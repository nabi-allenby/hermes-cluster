// Package sweeper runs the idle and TTL sweep loops — the only writers of
// lifecycle intent in the whole system.
package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

const cycleTimeout = 30 * time.Second

// reconcileEvery is how many sweep cycles pass between reconcile reports.
const reconcileEvery = 10

// Runner drives both sweepers on one jittered ticker.
type Runner struct {
	Manager   *lifecycle.Manager
	Interval  time.Duration
	Reconcile *reconcile.Store
	Log       *slog.Logger

	// IdleHorizon and WakeBootMargin drive the cron half of Idle v2: a
	// cron fire within the horizon blocks the suspend; beyond it the
	// session suspends with a wake scheduled bootMargin before the fire.
	IdleHorizon    time.Duration
	WakeBootMargin time.Duration

	// Exposure, when non-nil, makes each sweep ensure per-session
	// Services (and dashboard Ingresses) exist — see exposure.go.
	Exposure *ExposureConfig

	// quietSince is the Idle v2 quiet clock, keyed by claim name: when the
	// LM first observed the agent quiet, with no busy or unreachable
	// observation since. In-memory on purpose (the LM is stateless): a
	// restart resets the clocks, which only delays suspends — fail closed.
	quietSince map[string]time.Time
}

// Run blocks until ctx is done. The first sweep (and reconcile) happens
// immediately at startup — a restarted lifecycle-manager rebuilds its whole
// view from the claim list + instance list — statelessness by design.
func (r *Runner) Run(ctx context.Context) {
	cycle := 0
	for {
		r.sweep(ctx, cycle%reconcileEvery == 0)
		cycle++
		jitter := time.Duration(rand.Int63n(int64(r.Interval) / 10)) // ±10 %
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.Interval - r.Interval/20 + jitter):
		}
	}
}

func (r *Runner) sweep(parent context.Context, doReconcile bool) {
	ctx, cancel := context.WithTimeout(parent, cycleTimeout)
	defer cancel()

	claims, err := r.Manager.K8s.ListClaims(ctx)
	if err != nil {
		r.Log.Error("sweep: claim list failed", "error", err)
		return
	}

	// One instance list per cycle; nil = disabled, throttled, or unreachable.
	instances := r.Manager.Instances(ctx)

	now := time.Now()
	for i := range claims {
		claim := &claims[i]
		if claim.Terminating {
			continue
		}
		r.sweepWake(ctx, claim, now)
		r.sweepBufferedWake(ctx, claim, instances)
		r.sweepTTL(ctx, claim, now)
		r.sweepIdle(ctx, claim, instances, now)
		if r.Exposure != nil {
			r.sweepExposure(ctx, claim, r.Manager.Sandbox(ctx, claim))
		}
	}
	r.pruneQuiet(claims)

	if doReconcile && r.Reconcile != nil && r.Manager.Connector.Enabled() {
		r.Reconcile.Update(claims, instances)
	}
}

// sweepWake processes scheduled wakes (issue #2, Wake v2 trigger 3): a
// suspended session whose wake-at has passed is resumed so its cron can
// fire. Manager.Wake clears the annotation on success.
func (r *Runner) sweepWake(ctx context.Context, claim *k8s.Claim, now time.Time) {
	raw := claim.Annotations[session.AnnoWakeAt]
	if raw == "" {
		return
	}
	wakeAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Only the sweeper writes this annotation; a malformed value is
		// hand-edited junk. Clear it rather than log-spamming every sweep.
		r.Log.Warn("clearing malformed wake-at annotation", "session", claim.Name, "value", raw)
		r.clearWakeAt(ctx, claim.Name)
		return
	}
	sb := r.Manager.Sandbox(ctx, claim)
	if sb == nil {
		return
	}
	if !sb.Suspended && sb.OperatingMode != "Suspended" {
		// Already running (woken by Discord or /wake) — the schedule is stale.
		r.clearWakeAt(ctx, claim.Name)
		return
	}
	if now.Before(wakeAt) {
		return
	}
	r.Log.Info("scheduled wake due; resuming", "session", claim.Name, "wakeAt", wakeAt)
	if err := r.Manager.Wake(ctx, claim.Name); err != nil {
		r.Log.Error("scheduled wake failed; will retry next sweep", "session", claim.Name, "error", err)
	}
}

// sweepBufferedWake wakes a suspended session whose connector instance has
// undelivered buffered messages — the backstop for a lost wake poke (#20).
// The connector pokes only on the buffer's empty→non-empty transition and
// counts any HTTP response as sent, so a single transient failure at wake
// time (LM restart, API-server blip) would otherwise strand the session
// asleep with pending messages until manual intervention. Stateless and
// idempotent: the sweep cadence is the retry loop, and the idle sweeper's
// "nothing buffered" guard prevents a suspend/wake flap.
func (r *Runner) sweepBufferedWake(ctx context.Context, claim *k8s.Claim, instances map[string]connector.Instance) {
	if claim.Annotations[session.AnnoConnector] != "true" || instances == nil {
		return
	}
	inst, ok := instances[claim.Name]
	if !ok || inst.Revoked || inst.BufferedCount == 0 {
		return
	}
	sb := r.Manager.Sandbox(ctx, claim)
	// operatingMode covers Suspending too: a message that raced an in-flight
	// suspend should also wake, and the patch is safe mid-suspension.
	if sb == nil || sb.OperatingMode != "Suspended" {
		return
	}
	r.Log.Info("buffered messages pending for suspended session; waking",
		"session", claim.Name, "buffered", inst.BufferedCount)
	if err := r.Manager.Wake(ctx, claim.Name); err != nil {
		r.Log.Error("buffered wake failed; will retry next sweep", "session", claim.Name, "error", err)
	}
}

func (r *Runner) clearWakeAt(ctx context.Context, name string) {
	if err := r.Manager.K8s.PatchClaimAnnotations(ctx, name, map[string]*string{session.AnnoWakeAt: nil}); err != nil {
		r.Log.Warn("wake-at clear failed", "session", name, "error", err)
	}
}

func (r *Runner) sweepTTL(ctx context.Context, claim *k8s.Claim, now time.Time) {
	ttl := r.Manager.EffectiveTTL(claim)
	if ttl <= 0 || claim.CreatedAt.IsZero() || now.Sub(claim.CreatedAt) < ttl {
		return
	}
	r.Log.Info("TTL expired; decommissioning", "session", claim.Name, "ttl", ttl, "age", now.Sub(claim.CreatedAt))
	if _, err := r.Manager.Decommission(ctx, claim.Name); err != nil {
		if errors.Is(err, connector.ErrThrottled) {
			r.Log.Warn("TTL decommission deferred: connector throttled", "session", claim.Name)
		} else {
			r.Log.Error("TTL decommission failed; will retry next sweep", "session", claim.Name, "error", err)
		}
	}
}

func (r *Runner) sweepIdle(ctx context.Context, claim *k8s.Claim, instances map[string]connector.Instance, now time.Time) {
	if claim.Annotations[session.AnnoConnector] != "true" || instances == nil {
		// No activity signal without the connector: never guess at idleness.
		return
	}
	var inst *connector.Instance
	if got, ok := instances[claim.Name]; ok {
		inst = &got
	}
	sb := r.Manager.Sandbox(ctx, claim)
	in := IdleInput{
		Sandbox:     sb,
		Instance:    inst,
		IdleTimeout: r.Manager.EffectiveIdleTimeout(claim),
		Now:         now,
	}

	agentCli := r.Manager.AgentClient()
	if agentCli.Enabled() {
		in.AgentEnabled = true
		// Poll every running session each sweep (not just relay-quiet ones)
		// so the quiet clock accumulates alongside the connector's — a
		// conditional poll would serialize the two waits into ~2× the
		// timeout. Suspended/unbound sandboxes are skipped: nothing to ask.
		if sb != nil && !sb.Suspended && session.Derive(session.Facts{
			HasSandbox: true, OperatingMode: sb.OperatingMode, Ready: sb.Ready, Suspended: sb.Suspended,
		}) == session.StateReady {
			in.Agent = r.pollAgent(ctx, agentCli, claim.Name, r.statusTarget(claim, sb))
		}
		in.QuietSince = r.trackQuiet(claim.Name, in.Agent, now)
	}

	if !DecideIdle(in) {
		return
	}

	// Cron gate (fail closed): with credentials configured, an unreadable
	// schedule blocks the suspend — unknown is not "no cron". Without
	// credentials the guard is skipped; the chart wires them by default.
	var wakeAt *time.Time
	if agentCli.Enabled() && agentCli.CronConfigured() {
		nextFire, err := agentCli.NextCronFire(ctx, r.statusTarget(claim, sb))
		if err != nil {
			r.Log.Warn("cron schedule unreadable; deferring suspend", "session", claim.Name, "error", err)
			return
		}
		ok, wa := DecideCron(nextFire, now, r.IdleHorizon, r.WakeBootMargin)
		if !ok {
			r.Log.Info("cron fire imminent; not suspending", "session", claim.Name, "nextFire", *nextFire)
			return
		}
		wakeAt = wa
	}
	if wakeAt != nil {
		// Stamp the schedule before suspending: if the annotation write
		// fails we must not suspend (the cron would be lost — fail closed).
		value := wakeAt.UTC().Format(time.RFC3339)
		if err := r.Manager.K8s.PatchClaimAnnotations(ctx, claim.Name, map[string]*string{session.AnnoWakeAt: &value}); err != nil {
			r.Log.Error("wake-at annotate failed; deferring suspend", "session", claim.Name, "error", err)
			return
		}
	}

	r.Log.Info("session idle; suspending", "session", claim.Name, "idleTimeout", in.IdleTimeout, "wakeAt", wakeAt)
	if err := r.Manager.K8s.PatchSandboxOperatingMode(ctx, in.Sandbox.Name, "Suspended"); err != nil {
		r.Log.Error("suspend patch failed", "session", claim.Name, "error", err)
	}
}

// pollAgent fetches one /api/status observation; nil = unknown (not idle).
func (r *Runner) pollAgent(ctx context.Context, cli agent.Client, name, target string) *agent.Report {
	report, err := cli.Status(ctx, target)
	if err != nil {
		r.Log.Warn("agent status poll failed", "session", name, "error", err)
		return nil
	}
	return report
}

// trackQuiet advances the per-session quiet clock: a quiet observation
// starts (or keeps) the clock, anything else resets it.
func (r *Runner) trackQuiet(name string, report *agent.Report, now time.Time) time.Time {
	if r.quietSince == nil {
		r.quietSince = map[string]time.Time{}
	}
	if report == nil || report.Busy() {
		delete(r.quietSince, name)
		return time.Time{}
	}
	if since, ok := r.quietSince[name]; ok {
		return since
	}
	r.quietSince[name] = now
	return now
}

// pruneQuiet drops quiet-clock entries for claims that no longer exist.
func (r *Runner) pruneQuiet(claims []k8s.Claim) {
	if r.quietSince == nil {
		return
	}
	alive := make(map[string]bool, len(claims))
	for i := range claims {
		alive[claims[i].Name] = true
	}
	for name := range r.quietSince {
		if !alive[name] {
			delete(r.quietSince, name)
		}
	}
}

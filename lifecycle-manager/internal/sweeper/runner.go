// Package sweeper runs the idle and TTL sweep loops — the only writers of
// lifecycle intent in the whole system.
package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

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
		r.sweepTTL(ctx, claim, now)
		r.sweepIdle(ctx, claim, instances, now)
	}

	if doReconcile && r.Reconcile != nil && r.Manager.Connector.Enabled() {
		r.Reconcile.Update(claims, instances)
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
	in := IdleInput{
		Sandbox:     r.Manager.Sandbox(ctx, claim),
		Instance:    inst,
		IdleTimeout: r.Manager.EffectiveIdleTimeout(claim),
		Now:         now,
	}
	if !DecideIdle(in) {
		return
	}
	r.Log.Info("session idle; suspending", "session", claim.Name, "idleTimeout", in.IdleTimeout)
	if err := r.Manager.K8s.PatchSandboxOperatingMode(ctx, in.Sandbox.Name, "Suspended"); err != nil {
		r.Log.Error("suspend patch failed", "session", claim.Name, "error", err)
	}
}

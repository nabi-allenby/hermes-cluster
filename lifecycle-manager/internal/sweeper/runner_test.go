package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

type sweepConnector struct {
	connector.Disabled
	enabled   bool
	instances []connector.Instance
	deletes   []string
}

func (s *sweepConnector) Enabled() bool { return s.enabled }
func (s *sweepConnector) ListInstances(context.Context) ([]connector.Instance, error) {
	return s.instances, nil
}
func (s *sweepConnector) DeleteInstance(_ context.Context, id string) error {
	s.deletes = append(s.deletes, id)
	return nil
}

// sweepAgent is a scripted agent.Client: reports and cron fires keyed by
// target (the Fake binds ServiceFQDN == session name).
type sweepAgent struct {
	agent.Disabled
	reports  map[string]*agent.Report // absent key = unreachable
	nextFire map[string]*time.Time
	cronCred bool
	cronErr  error
}

func (a *sweepAgent) Enabled() bool        { return true }
func (a *sweepAgent) CronConfigured() bool { return a.cronCred }
func (a *sweepAgent) Status(_ context.Context, target string) (*agent.Report, error) {
	if r, ok := a.reports[target]; ok {
		return r, nil
	}
	return nil, errors.New("connection refused")
}
func (a *sweepAgent) NextCronFire(_ context.Context, target string) (*time.Time, error) {
	if a.cronErr != nil {
		return nil, a.cronErr
	}
	return a.nextFire[target], nil
}

func newRunner(fake *k8s.Fake, conn connector.Client) (*Runner, *reconcile.Store) {
	log := slog.New(slog.DiscardHandler)
	manager := &lifecycle.Manager{
		K8s: fake, Connector: conn,
		Defaults: lifecycle.Defaults{WarmPool: "pool", TTL: 0, IdleTimeout: 30 * time.Minute},
		Log:      log,
	}
	store := &reconcile.Store{}
	return &Runner{
		Manager: manager, Interval: time.Minute, Reconcile: store, Log: log,
		IdleHorizon: 5 * time.Minute, WakeBootMargin: 2 * time.Minute,
	}, store
}

// relayIdle builds a connector instance that passes every Idle v1 guard.
func relayIdle(name string) connector.Instance {
	old := time.Now().Add(-2 * time.Hour).Unix()
	return connector.Instance{GatewayID: name, LastInboundAt: &old, LastOutboundAt: &old}
}

func TestTTLSweepDecommissions(t *testing.T) {
	fake := &k8s.Fake{}
	// TTL 3600s, created 2h ago -> expired. Connector-provisioned session.
	fake.AddSession("s-old", time.Now().Add(-2*time.Hour),
		map[string]string{"hermes.nabi.dev/ttl-seconds": "3600", "hermes.nabi.dev/connector": "true"}, "Running", true)
	// No TTL annotation, global TTL 0 -> immortal.
	fake.AddSession("s-forever", time.Now().Add(-100*time.Hour), nil, "Running", true)

	conn := &sweepConnector{enabled: true}
	runner, _ := newRunner(fake, conn)
	runner.sweep(context.Background(), false)

	if _, err := fake.GetClaim(context.Background(), "s-old"); err == nil {
		t.Fatal("expired session should be gone")
	}
	if len(conn.deletes) != 1 || conn.deletes[0] != "s-old" {
		t.Fatalf("connector deprovision calls: %v", conn.deletes)
	}
	if _, err := fake.GetClaim(context.Background(), "s-forever"); err != nil {
		t.Fatal("session without TTL must survive")
	}
}

func TestIdleSweepSuspends(t *testing.T) {
	fake := &k8s.Fake{}
	created := time.Now().Add(-3 * time.Hour)
	fake.AddSession("s-idle", created, map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
	fake.AddSession("s-busy", created, map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
	fake.AddSession("s-nocon", created, nil, "Running", true)

	old := time.Now().Add(-2 * time.Hour).Unix()
	recent := time.Now().Add(-time.Minute).Unix()
	conn := &sweepConnector{enabled: true, instances: []connector.Instance{
		{GatewayID: "s-idle", LastInboundAt: &old, LastOutboundAt: &old},
		{GatewayID: "s-busy", LastInboundAt: &recent, LastOutboundAt: &recent},
	}}
	runner, store := newRunner(fake, conn)
	runner.sweep(context.Background(), true)

	for name, wantMode := range map[string]string{"s-idle": "Suspended", "s-busy": "Running", "s-nocon": "Running"} {
		sb, err := fake.GetSandbox(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sb.OperatingMode != wantMode {
			t.Errorf("%s: mode %q, want %q", name, sb.OperatingMode, wantMode)
		}
	}

	report, ok := store.Load()
	if !ok {
		t.Fatal("reconcile report missing")
	}
	// s-nocon is managed but not connector-flagged: not an orphan on either side.
	if len(report.ClaimsWithoutInstances) != 0 || len(report.InstancesWithoutClaims) != 0 {
		t.Fatalf("unexpected orphans: %+v", report)
	}
}

// TestIdleSweepRespectsAgentActivity is the issue #2 regression test: relay
// silence alone must not suspend when the agent reports work in progress.
func TestIdleSweepRespectsAgentActivity(t *testing.T) {
	fake := &k8s.Fake{}
	created := time.Now().Add(-3 * time.Hour)
	anno := map[string]string{"hermes.nabi.dev/connector": "true"}
	for _, name := range []string{"s-quiet", "s-workflow", "s-desktop", "s-unreach", "s-fresh"} {
		fake.AddSession(name, created, anno, "Running", true)
	}

	conn := &sweepConnector{enabled: true, instances: []connector.Instance{
		relayIdle("s-quiet"), relayIdle("s-workflow"), relayIdle("s-desktop"),
		relayIdle("s-unreach"), relayIdle("s-fresh"),
	}}
	runner, _ := newRunner(fake, conn)
	runner.Manager.Agent = &sweepAgent{reports: map[string]*agent.Report{
		"s-quiet":    {},
		"s-workflow": {ActiveAgents: 1},   // long workflow, relay-silent mid-run
		"s-desktop":  {ActiveSessions: 1}, // desktop chat, invisible to the relay
		"s-fresh":    {},
		// s-unreach: no report -> unreachable -> unknown -> not idle
	}}
	old := time.Now().Add(-time.Hour)
	runner.quietSince = map[string]time.Time{
		"s-quiet": old,
		"s-fresh": time.Now().Add(-time.Minute), // quiet, but not long enough
	}
	runner.sweep(context.Background(), false)

	want := map[string]string{
		"s-quiet":    "Suspended",
		"s-workflow": "Running",
		"s-desktop":  "Running",
		"s-unreach":  "Running",
		"s-fresh":    "Running",
	}
	for name, wantMode := range want {
		sb, err := fake.GetSandbox(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sb.OperatingMode != wantMode {
			t.Errorf("%s: mode %q, want %q", name, sb.OperatingMode, wantMode)
		}
	}
	// Busy/unreachable observations must reset the quiet clock.
	if _, ok := runner.quietSince["s-workflow"]; ok {
		t.Error("busy session kept a quiet clock")
	}
	if _, ok := runner.quietSince["s-unreach"]; ok {
		t.Error("unreachable session kept a quiet clock")
	}
}

func TestIdleSweepCronGate(t *testing.T) {
	newIdleFake := func(name string) (*k8s.Fake, *sweepConnector) {
		fake := &k8s.Fake{}
		fake.AddSession(name, time.Now().Add(-3*time.Hour),
			map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
		return fake, &sweepConnector{enabled: true, instances: []connector.Instance{relayIdle(name)}}
	}
	quietFor := func(r *Runner, name string) {
		r.quietSince = map[string]time.Time{name: time.Now().Add(-time.Hour)}
	}

	t.Run("imminent fire blocks suspend", func(t *testing.T) {
		fake, conn := newIdleFake("s-cron")
		runner, _ := newRunner(fake, conn)
		fire := time.Now().Add(3 * time.Minute) // inside the 5m horizon
		runner.Manager.Agent = &sweepAgent{
			reports:  map[string]*agent.Report{"s-cron": {}},
			nextFire: map[string]*time.Time{"s-cron": &fire},
			cronCred: true,
		}
		quietFor(runner, "s-cron")
		runner.sweep(context.Background(), false)

		sb, _ := fake.GetSandbox(context.Background(), "s-cron")
		if sb.OperatingMode == "Suspended" {
			t.Fatal("suspended despite an imminent cron fire")
		}
	})

	t.Run("distant fire suspends with wake-at", func(t *testing.T) {
		fake, conn := newIdleFake("s-cron")
		runner, _ := newRunner(fake, conn)
		fire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		runner.Manager.Agent = &sweepAgent{
			reports:  map[string]*agent.Report{"s-cron": {}},
			nextFire: map[string]*time.Time{"s-cron": &fire},
			cronCred: true,
		}
		quietFor(runner, "s-cron")
		runner.sweep(context.Background(), false)

		sb, _ := fake.GetSandbox(context.Background(), "s-cron")
		if sb.OperatingMode != "Suspended" {
			t.Fatal("expected suspend with a scheduled wake")
		}
		claim, _ := fake.GetClaim(context.Background(), "s-cron")
		got := claim.Annotations[session.AnnoWakeAt]
		want := fire.Add(-2 * time.Minute).Format(time.RFC3339)
		if got != want {
			t.Fatalf("wake-at %q, want %q", got, want)
		}
	})

	t.Run("unreadable schedule defers suspend", func(t *testing.T) {
		fake, conn := newIdleFake("s-cron")
		runner, _ := newRunner(fake, conn)
		runner.Manager.Agent = &sweepAgent{
			reports:  map[string]*agent.Report{"s-cron": {}},
			cronCred: true,
			cronErr:  errors.New("401 unauthorized"),
		}
		quietFor(runner, "s-cron")
		runner.sweep(context.Background(), false)

		sb, _ := fake.GetSandbox(context.Background(), "s-cron")
		if sb.OperatingMode == "Suspended" {
			t.Fatal("suspended although the cron schedule was unknown (must fail closed)")
		}
	})

	t.Run("no credentials skips the cron gate", func(t *testing.T) {
		fake, conn := newIdleFake("s-cron")
		runner, _ := newRunner(fake, conn)
		runner.Manager.Agent = &sweepAgent{
			reports:  map[string]*agent.Report{"s-cron": {}},
			cronCred: false,
			cronErr:  errors.New("must not be called"),
		}
		quietFor(runner, "s-cron")
		runner.sweep(context.Background(), false)

		sb, _ := fake.GetSandbox(context.Background(), "s-cron")
		if sb.OperatingMode != "Suspended" {
			t.Fatal("expected suspend when the cron gate is unconfigured")
		}
	})
}

// TestBufferedWakeBackstop is the issue #20 regression test: a suspended
// session with undelivered buffered messages must be woken by the sweep loop
// — the connector's wake poke is single-shot per suspension epoch, so a lost
// poke would otherwise strand the session until manual intervention.
func TestBufferedWakeBackstop(t *testing.T) {
	anno := map[string]string{"hermes.nabi.dev/connector": "true"}
	buffered := func(name string, count int, revoked bool) connector.Instance {
		inst := relayIdle(name)
		inst.BufferedCount = count
		inst.Revoked = revoked
		return inst
	}

	fake := &k8s.Fake{}
	created := time.Now().Add(-3 * time.Hour)
	fake.AddSession("s-strand", created, anno, "Suspended", false)
	fake.AddSession("s-empty", created, anno, "Suspended", false)
	fake.AddSession("s-revoked", created, anno, "Suspended", false)
	fake.AddSession("s-running", created, anno, "Running", true)
	fake.AddSession("s-nocon", created, nil, "Suspended", false)

	conn := &sweepConnector{enabled: true, instances: []connector.Instance{
		buffered("s-strand", 2, false),
		buffered("s-empty", 0, false),
		buffered("s-revoked", 3, true),
		// s-running: buffered while up (gateway mid-reconnect) — not ours to
		// touch. nil activity timestamps keep the idle sweeper off it too.
		{GatewayID: "s-running", BufferedCount: 1},
	}}
	runner, _ := newRunner(fake, conn)
	runner.sweep(context.Background(), false)

	want := map[string]string{
		"s-strand":  "Running",   // the backstop: buffered + suspended -> woken
		"s-empty":   "Suspended", // nothing pending
		"s-revoked": "Suspended", // revoked instances are never woken
		"s-running": "Running",   // already up; untouched
		"s-nocon":   "Suspended", // not connector-managed
	}
	for name, wantMode := range want {
		sb, err := fake.GetSandbox(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sb.OperatingMode != wantMode {
			t.Errorf("%s: mode %q, want %q", name, sb.OperatingMode, wantMode)
		}
	}
}

// TestBufferedWakeUnknownInstances: a throttled/unreachable connector yields
// nil instances — the backstop must do nothing rather than guess.
func TestBufferedWakeUnknownInstances(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-strand", time.Now().Add(-3*time.Hour),
		map[string]string{"hermes.nabi.dev/connector": "true"}, "Suspended", false)

	runner, _ := newRunner(fake, &sweepConnector{enabled: false})
	runner.sweep(context.Background(), false)

	sb, err := fake.GetSandbox(context.Background(), "s-strand")
	if err != nil {
		t.Fatal(err)
	}
	if sb.OperatingMode != "Suspended" {
		t.Fatalf("woke a session with no instance data; mode %q", sb.OperatingMode)
	}
}

func TestScheduledWakeProcessing(t *testing.T) {
	anno := func(wakeAt string) map[string]string {
		return map[string]string{
			"hermes.nabi.dev/connector": "true",
			session.AnnoWakeAt:          wakeAt,
		}
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	fake := &k8s.Fake{}
	fake.AddSession("s-due", time.Now().Add(-3*time.Hour), anno(past), "Suspended", false)
	fake.AddSession("s-later", time.Now().Add(-3*time.Hour), anno(future), "Suspended", false)
	fake.AddSession("s-junk", time.Now().Add(-3*time.Hour), anno("not-a-time"), "Suspended", false)
	fake.AddSession("s-stale", time.Now().Add(-3*time.Hour), anno(past), "Running", true)

	conn := &sweepConnector{enabled: true}
	runner, _ := newRunner(fake, conn)
	runner.sweep(context.Background(), false)

	sb, _ := fake.GetSandbox(context.Background(), "s-due")
	if sb.OperatingMode != "Running" {
		t.Error("due scheduled wake did not resume the session")
	}
	claim, _ := fake.GetClaim(context.Background(), "s-due")
	if claim.Annotations[session.AnnoWakeAt] != "" {
		t.Error("wake-at not cleared after the scheduled wake")
	}

	sb, _ = fake.GetSandbox(context.Background(), "s-later")
	if sb.OperatingMode != "Suspended" {
		t.Error("future scheduled wake fired early")
	}

	claim, _ = fake.GetClaim(context.Background(), "s-junk")
	if claim.Annotations[session.AnnoWakeAt] != "" {
		t.Error("malformed wake-at not cleared")
	}
	sb, _ = fake.GetSandbox(context.Background(), "s-junk")
	if sb.OperatingMode != "Suspended" {
		t.Error("malformed wake-at must not wake the session")
	}

	claim, _ = fake.GetClaim(context.Background(), "s-stale")
	if claim.Annotations[session.AnnoWakeAt] != "" {
		t.Error("stale wake-at on a running session not cleared")
	}
}

func TestReconcileReportsOrphans(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-lost", time.Now(), map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
	conn := &sweepConnector{enabled: true, instances: []connector.Instance{
		{GatewayID: "manual-gw"},
	}}
	runner, store := newRunner(fake, conn)
	runner.sweep(context.Background(), true)

	report, _ := store.Load()
	if len(report.ClaimsWithoutInstances) != 1 || report.ClaimsWithoutInstances[0] != "s-lost" {
		t.Fatalf("claimsWithoutInstances: %v", report.ClaimsWithoutInstances)
	}
	if len(report.InstancesWithoutClaims) != 1 || report.InstancesWithoutClaims[0] != "manual-gw" {
		t.Fatalf("instancesWithoutClaims: %v", report.InstancesWithoutClaims)
	}
	// Report-only: the out-of-band instance must NOT have been deleted.
	if len(conn.deletes) != 0 {
		t.Fatalf("orphan policy must be report-only; deletes: %v", conn.deletes)
	}
}

package sweeper

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
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

func newRunner(fake *k8s.Fake, conn connector.Client) (*Runner, *reconcile.Store) {
	log := slog.New(slog.DiscardHandler)
	manager := &lifecycle.Manager{
		K8s: fake, Connector: conn,
		Defaults: lifecycle.Defaults{WarmPool: "pool", TTL: 0, IdleTimeout: 30 * time.Minute},
		Log:      log,
	}
	store := &reconcile.Store{}
	return &Runner{Manager: manager, Interval: time.Minute, Reconcile: store, Log: log}, store
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

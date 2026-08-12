package sweeper

import (
	"context"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
)

func testExposure() *ExposureConfig {
	return &ExposureConfig{
		Port:         9119,
		Domain:       "hermes.example.com",
		IngressClass: "webapprouting.kubernetes.azure.com",
		TLSSecret:    "dashboard-wildcard-tls",
		DenyService:  "hermes-dashboard-deny",
	}
}

// bindSelector rebinds a seeded session's sandbox the way agent-sandbox
// v0.5.4 actually reports it: selector published, serviceFQDN NEVER set
// (issue #11).
func bindSelector(fake *k8s.Fake, name string, readyFor time.Duration) {
	fake.SetSandbox(name, k8s.Sandbox{
		Name: name, OperatingMode: "Running", Ready: true,
		ReadyChanged: time.Now().Add(-readyFor),
		Selector:     "agents.x-k8s.io/sandbox-name-hash=abc123",
	})
}

func TestSweepCreatesExposureObjects(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-dc-185862919387480064", time.Now().Add(-time.Hour), nil, "Running", true)
	fake.AddSession("s-abc1234567", time.Now().Add(-time.Hour), nil, "Running", true)
	fake.AddSession("s-unbound", time.Now().Add(-time.Hour), nil, "Running", true)
	bindSelector(fake, "s-dc-185862919387480064", time.Hour)
	bindSelector(fake, "s-abc1234567", time.Hour)
	// s-unbound keeps AddSession's sandbox: no selector published yet.
	fake.SetSandbox("s-unbound", k8s.Sandbox{Name: "s-unbound", OperatingMode: "Running", Ready: true})

	runner, _ := newRunner(fake, &sweepConnector{})
	runner.Exposure = testExposure()
	runner.sweep(context.Background(), false)

	// Discord-provisioned session: Service + Ingress with per-session host
	// and the deny-list backend.
	spec, ok := fake.Exposure("s-dc-185862919387480064")
	if !ok {
		t.Fatal("no exposure for the s-dc session")
	}
	if spec.Host != "s-dc-185862919387480064.hermes.example.com" {
		t.Fatalf("host: %q", spec.Host)
	}
	if spec.DenyService != "hermes-dashboard-deny" || spec.TLSSecret != "dashboard-wildcard-tls" {
		t.Fatalf("ingress spec incomplete: %+v", spec)
	}
	if spec.Port != 9119 || spec.Selector == "" {
		t.Fatalf("service spec incomplete: %+v", spec)
	}

	// API-created session: Service only — no owner identity the broker
	// could authorize, so no public hostname (PLAN T4).
	spec, ok = fake.Exposure("s-abc1234567")
	if !ok {
		t.Fatal("no exposure for the API-created session")
	}
	if spec.Host != "" {
		t.Fatalf("API-created session must not get an Ingress host, got %q", spec.Host)
	}

	// Unbound sandbox: nothing yet; the next sweep retries.
	if _, ok := fake.Exposure("s-unbound"); ok {
		t.Fatal("exposure created before the controller published a selector")
	}
}

func TestSweepExposureServiceOnlyWithoutDomain(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-dc-1", time.Now().Add(-time.Hour), nil, "Running", true)
	bindSelector(fake, "s-dc-1", time.Hour)

	runner, _ := newRunner(fake, &sweepConnector{})
	runner.Exposure = &ExposureConfig{Port: 9119} // no Domain
	runner.sweep(context.Background(), false)

	spec, ok := fake.Exposure("s-dc-1")
	if !ok {
		t.Fatal("service-only exposure missing")
	}
	if spec.Host != "" {
		t.Fatalf("no domain configured, but got host %q", spec.Host)
	}
}

// Issue #11 regression, both halves. agent-sandbox v0.5.4 never populates
// status.serviceFQDN, so the status pollers need the exposure Service as
// their target; without it, Idle v2 can never observe the agent and no
// session ever suspends.
func TestIdleV2SuspendsViaExposureServiceTarget(t *testing.T) {
	name := "s-dc-1"
	quiet := &agent.Report{ActiveAgents: 0, ActiveSessions: 0}

	run := func(exposure *ExposureConfig) *k8s.Fake {
		fake := &k8s.Fake{}
		fake.AddSession(name, time.Now().Add(-2*time.Hour),
			map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
		fake.SetSandbox(name, k8s.Sandbox{
			Name: name, OperatingMode: "Running", Ready: true,
			ReadyChanged: time.Now().Add(-2 * time.Hour),
			Selector:     "agents.x-k8s.io/sandbox-name-hash=abc123",
			// ServiceFQDN deliberately empty — the v0.5.4 reality.
		})

		conn := &sweepConnector{enabled: true, instances: []connector.Instance{relayIdle(name)}}
		runner, _ := newRunner(fake, conn)
		runner.Manager.Agent = &sweepAgent{reports: map[string]*agent.Report{name: quiet}}
		runner.Exposure = exposure
		// Pre-earned quiet clock: the sweeper has observed quiet for the
		// full timeout already.
		runner.quietSince = map[string]time.Time{name: time.Now().Add(-time.Hour)}
		runner.sweep(context.Background(), false)
		return fake
	}

	// With exposure: the poll target falls back to the Service name and
	// the idle decision completes -> suspended.
	fake := run(testExposure())
	sb, _ := fake.GetSandbox(context.Background(), name)
	if sb.OperatingMode != "Suspended" {
		t.Fatalf("with exposure Services the session must suspend; mode=%q", sb.OperatingMode)
	}

	// Without exposure: no target exists, the poll fails closed, and the
	// session never suspends — the exact issue #11 behavior.
	fake = run(nil)
	sb, _ = fake.GetSandbox(context.Background(), name)
	if sb.OperatingMode == "Suspended" {
		t.Fatal("without a poll target the sweeper must fail closed, not suspend")
	}
}

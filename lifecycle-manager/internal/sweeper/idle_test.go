package sweeper

import (
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
)

func TestDecideIdle(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	oldUnix := old.Unix()
	fresh := now.Add(-time.Minute).Unix()
	timeout := 30 * time.Minute

	readySandbox := func() *k8s.Sandbox {
		return &k8s.Sandbox{Name: "s", OperatingMode: "Running", Ready: true, ReadyChanged: old}
	}
	idleInstance := func() *connector.Instance {
		return &connector.Instance{GatewayID: "s", Connected: true, LastInboundAt: &oldUnix, LastOutboundAt: &oldUnix}
	}

	base := func() IdleInput {
		return IdleInput{Sandbox: readySandbox(), Instance: idleInstance(), IdleTimeout: timeout, Now: now}
	}

	t.Run("all guards pass -> suspend", func(t *testing.T) {
		if !DecideIdle(base()) {
			t.Fatal("expected suspend")
		}
	})

	tests := []struct {
		name   string
		mutate func(*IdleInput)
	}{
		{"timeout disabled", func(in *IdleInput) { in.IdleTimeout = 0 }},
		{"no sandbox", func(in *IdleInput) { in.Sandbox = nil }},
		{"sandbox suspended already", func(in *IdleInput) { in.Sandbox.OperatingMode = "Suspended"; in.Sandbox.Suspended = true }},
		{"sandbox suspending", func(in *IdleInput) { in.Sandbox.OperatingMode = "Suspended" }},
		{"sandbox waking (not ready)", func(in *IdleInput) { in.Sandbox.Ready = false }},
		{"fresh resume: Ready transition newer than timeout", func(in *IdleInput) { in.Sandbox.ReadyChanged = now.Add(-time.Minute) }},
		{"ready transition unknown", func(in *IdleInput) { in.Sandbox.ReadyChanged = time.Time{} }},
		{"no connector instance", func(in *IdleInput) { in.Instance = nil }},
		{"instance revoked", func(in *IdleInput) { in.Instance.Revoked = true }},
		{"turn in flight", func(in *IdleInput) { in.Instance.TurnInFlight = true }},
		{"lastInboundAt nil (connector restarted)", func(in *IdleInput) { in.Instance.LastInboundAt = nil }},
		{"lastOutboundAt nil", func(in *IdleInput) { in.Instance.LastOutboundAt = nil }},
		{"recent inbound", func(in *IdleInput) { in.Instance.LastInboundAt = &fresh }},
		{"recent outbound", func(in *IdleInput) { in.Instance.LastOutboundAt = &fresh }},
		{"buffered work pending", func(in *IdleInput) { in.Instance.BufferedCount = 3 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			if DecideIdle(in) {
				t.Fatalf("guard %q failed to block suspend", tt.name)
			}
		})
	}

	t.Run("boundary: exactly at timeout suspends", func(t *testing.T) {
		in := base()
		at := now.Add(-timeout).Unix()
		in.Instance.LastInboundAt = &at
		in.Instance.LastOutboundAt = &at
		if !DecideIdle(in) {
			t.Fatal("expected suspend exactly at the timeout boundary")
		}
	})

	// Idle v2 (issue #2): agent-reported activity guards.
	baseV2 := func() IdleInput {
		in := base()
		in.AgentEnabled = true
		in.Agent = &agent.Report{}
		in.QuietSince = old
		return in
	}

	t.Run("v2: all guards pass -> suspend", func(t *testing.T) {
		if !DecideIdle(baseV2()) {
			t.Fatal("expected suspend")
		}
	})

	v2tests := []struct {
		name   string
		mutate func(*IdleInput)
	}{
		{"agent unreachable", func(in *IdleInput) { in.Agent = nil }},
		{"agent turn in flight (long workflow)", func(in *IdleInput) { in.Agent.ActiveAgents = 1 }},
		{"recently active session (cron/desktop)", func(in *IdleInput) { in.Agent.ActiveSessions = 1 }},
		{"quiet clock unknown (LM restarted)", func(in *IdleInput) { in.QuietSince = time.Time{} }},
		{"quiet clock younger than timeout", func(in *IdleInput) { in.QuietSince = now.Add(-time.Minute) }},
	}
	for _, tt := range v2tests {
		t.Run("v2: "+tt.name, func(t *testing.T) {
			in := baseV2()
			tt.mutate(&in)
			if DecideIdle(in) {
				t.Fatalf("guard %q failed to block suspend", tt.name)
			}
		})
	}

	t.Run("v2: polling disabled ignores agent fields", func(t *testing.T) {
		in := base() // AgentEnabled false, Agent nil, QuietSince zero
		if !DecideIdle(in) {
			t.Fatal("v1 behavior must be preserved when status polling is off")
		}
	})
}

func TestDecideCron(t *testing.T) {
	now := time.Now()
	horizon := 5 * time.Minute
	margin := 2 * time.Minute

	t.Run("no cron scheduled -> suspend, no wake", func(t *testing.T) {
		ok, wakeAt := DecideCron(nil, now, horizon, margin)
		if !ok || wakeAt != nil {
			t.Fatalf("ok=%v wakeAt=%v", ok, wakeAt)
		}
	})

	t.Run("fire inside horizon -> no suspend", func(t *testing.T) {
		fire := now.Add(3 * time.Minute)
		ok, _ := DecideCron(&fire, now, horizon, margin)
		if ok {
			t.Fatal("imminent cron fire must block the suspend")
		}
	})

	t.Run("fire beyond horizon -> suspend with margin-adjusted wake", func(t *testing.T) {
		fire := now.Add(time.Hour)
		ok, wakeAt := DecideCron(&fire, now, horizon, margin)
		if !ok || wakeAt == nil {
			t.Fatalf("ok=%v wakeAt=%v", ok, wakeAt)
		}
		if want := fire.Add(-margin); !wakeAt.Equal(want) {
			t.Fatalf("wakeAt %v, want %v", wakeAt, want)
		}
	})

	t.Run("boundary: fire exactly at horizon -> no suspend", func(t *testing.T) {
		fire := now.Add(horizon)
		if ok, _ := DecideCron(&fire, now, horizon, margin); ok {
			t.Fatal("fire at the horizon boundary must block")
		}
	})
}

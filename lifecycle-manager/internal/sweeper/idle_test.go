package sweeper

import (
	"testing"
	"time"

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
}

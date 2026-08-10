package sweeper

import (
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

// IdleInput bundles the observed facts for one session's suspend decision.
type IdleInput struct {
	Sandbox     *k8s.Sandbox
	Instance    *connector.Instance // nil = no instance for this gatewayId
	IdleTimeout time.Duration       // effective (annotation override applied)
	Now         time.Time
}

// DecideIdle is the conservative v1 suspend gate (design §4.4, "never
// mid-turn"). Pure function: returns true only when EVERY guard passes.
//
// Guards, in order:
//  1. idle sweeping enabled for this session (timeout > 0)
//  2. sandbox bound, operatingMode Running, Ready condition True — never
//     touch Pending/Waking/Suspending sessions
//  3. the Ready condition is older than the idle timeout — a freshly resumed
//     pod must not be re-suspended before its gateway produces activity
//  4. a connector instance exists and is not revoked
//  5. no turn in flight
//  6. both activity timestamps are known (non-nil) — nil means the connector
//     restarted and lost its in-memory activity; unknown is NOT idle
//  7. the newer of the two timestamps is older than the idle timeout
//  8. nothing is buffered — buffered work means the session is about to be
//     busy (and a suspend would race the wake poke)
func DecideIdle(in IdleInput) bool {
	if in.IdleTimeout <= 0 {
		return false
	}
	sb := in.Sandbox
	if sb == nil || sb.Suspended {
		return false
	}
	facts := session.Facts{HasSandbox: true, OperatingMode: sb.OperatingMode, Ready: sb.Ready, Suspended: sb.Suspended}
	if session.Derive(facts) != session.StateReady {
		return false
	}
	if sb.ReadyChanged.IsZero() || in.Now.Sub(sb.ReadyChanged) < in.IdleTimeout {
		return false
	}
	inst := in.Instance
	if inst == nil || inst.Revoked {
		return false
	}
	if inst.TurnInFlight {
		return false
	}
	if inst.LastInboundAt == nil || inst.LastOutboundAt == nil {
		return false
	}
	last := *inst.LastInboundAt
	if *inst.LastOutboundAt > last {
		last = *inst.LastOutboundAt
	}
	if in.Now.Sub(time.Unix(last, 0)) < in.IdleTimeout {
		return false
	}
	return inst.BufferedCount == 0
}

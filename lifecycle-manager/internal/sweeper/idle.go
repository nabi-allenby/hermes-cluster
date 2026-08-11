package sweeper

import (
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
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

	// Idle v2 (issue #2): agent-reported activity. The connector only sees
	// relay traffic; cron- and desktop-initiated work is invisible to it,
	// so relay silence alone must never suspend.
	AgentEnabled bool          // status polling configured; false = Idle v1 guards only
	Agent        *agent.Report // latest /api/status observation; nil = unreachable/unknown
	QuietSince   time.Time     // start of LM-observed continuous agent quiet; zero = unknown
}

// DecideIdle is the conservative suspend gate ("never
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
//
// Idle v2 guards (only when status polling is enabled; all fail closed):
//
//  9. the pod's /api/status was reachable this sweep — unreachable is NOT idle
//  10. the agent reports no in-flight turns and no recently-active sessions
//  11. the LM has observed continuous agent quiet for the full idle timeout —
//     the status readout has no last-activity timestamp, so the sweeper keeps
//     its own quiet clock (reset by any busy or unreachable observation, and
//     by LM restarts: a fresh LM re-earns the full timeout, conservatively)
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
	if inst.BufferedCount != 0 {
		return false
	}
	if in.AgentEnabled {
		if in.Agent == nil || in.Agent.Busy() {
			return false
		}
		if in.QuietSince.IsZero() || in.Now.Sub(in.QuietSince) < in.IdleTimeout {
			return false
		}
	}
	return true
}

// DecideCron is the cron half of the suspend gate (issue #2, Wake v2
// trigger 3). Given the earliest upcoming cron fire (nil = none scheduled),
// it returns whether suspending is allowed and, if so, the wake-at time to
// stamp on the claim (nil = no scheduled wake needed).
//
//   - a fire inside the idle horizon means the pod would need waking almost
//     immediately — suspending would be churn, so don't
//   - a fire beyond the horizon allows the suspend, scheduled to wake
//     bootMargin before the fire so the gateway is up when cron ticks
func DecideCron(nextFire *time.Time, now time.Time, horizon, bootMargin time.Duration) (ok bool, wakeAt *time.Time) {
	if nextFire == nil {
		return true, nil
	}
	if nextFire.Sub(now) <= horizon {
		return false, nil
	}
	t := nextFire.Add(-bootMargin)
	return true, &t
}

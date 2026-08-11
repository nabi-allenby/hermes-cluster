// Package agent polls a session pod's hermes dashboard surface — the
// agent-reported activity source for Idle v2 (hermes-private-cluster #2).
//
// The connector only sees relay traffic; work started by cron or the desktop
// dashboard is invisible to it, so relay silence must never be the sole
// suspend signal. The pod's `hermes serve` backend exposes exactly the
// readout an external lifecycle system needs:
//
//   - GET /api/status — public even when the dashboard auth gate is on
//     (upstream PUBLIC_API_PATHS). `active_agents` is the in-flight
//     gateway-turn count persisted at every turn boundary;
//     `active_sessions` counts sessions active within the last 300s.
//   - GET /api/cron/jobs?profile=all — behind the auth gate. Each job
//     carries a persisted `next_run_at`. Read with a session minted via
//     POST /auth/password-login against the chart's throwaway basic-auth
//     provider.
//
// Field names and auth behavior verified against the conformance pin
// (NousResearch/hermes-agent@244d296).
package agent

import (
	"context"
	"time"
)

// Report is one /api/status observation.
type Report struct {
	ActiveAgents   int `json:"active_agents"`
	ActiveSessions int `json:"active_sessions"`
}

// Busy reports whether the agent shows any sign of life the sweeper must
// respect. active_sessions carries a built-in 300s recency window, so a
// single quiet observation already implies five minutes of session silence.
func (r *Report) Busy() bool {
	return r.ActiveAgents > 0 || r.ActiveSessions > 0
}

// Client is the sweeper's view of a session pod's dashboard surface.
// Targets are service FQDNs (Sandbox status.serviceFQDN); the client owns
// scheme and port.
type Client interface {
	// Enabled reports whether status polling is configured at all. When
	// false the idle sweeper falls back to Idle v1 (connector-only) guards.
	Enabled() bool
	// Status fetches the public /api/status readout. Any error means
	// "unknown", which callers must treat as not idle (fail closed).
	Status(ctx context.Context, target string) (*Report, error)
	// CronConfigured reports whether credentials for the auth-gated cron
	// endpoint are present. Without them the cron guard is skipped.
	CronConfigured() bool
	// NextCronFire returns the earliest next_run_at among enabled cron jobs
	// across all profiles, or nil when nothing is scheduled. An error means
	// the schedule is unknown; callers must not suspend on unknown.
	NextCronFire(ctx context.Context, target string) (*time.Time, error)
}

// Disabled is the no-op Client used when status polling is off.
type Disabled struct{}

func (Disabled) Enabled() bool        { return false }
func (Disabled) CronConfigured() bool { return false }
func (Disabled) Status(context.Context, string) (*Report, error) {
	return nil, ErrDisabled
}
func (Disabled) NextCronFire(context.Context, string) (*time.Time, error) {
	return nil, ErrDisabled
}

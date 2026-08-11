// Package lifecycle implements the session operations shared by the HTTP API
// and the sweepers: create (with optional connector provisioning), read,
// wake, and the single decommission path.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/agent"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

// ErrConnectorDisabled is returned when a request needs the connector but
// the integration is off.
var ErrConnectorDisabled = errors.New("connector integration is disabled")

// instanceCacheTTL bounds admin-plane load from list/read endpoints.
const instanceCacheTTL = 5 * time.Second

// Defaults carry the global knobs sessions inherit when no annotation
// overrides them.
type Defaults struct {
	WarmPool    string
	TTL         time.Duration
	IdleTimeout time.Duration
	Platform    string
	BotID       string
	WakeBaseURL string
}

// RouteKey identifies one chat to bind to the session's gateway.
type RouteKey struct {
	Platform string // empty = the configured default platform
	ChatID   string
}

// CreateRequest is the input to Create.
type CreateRequest struct {
	ID                 string
	WarmPool           string
	TTLSeconds         *int64
	IdleTimeoutSeconds *int64
	DisplayName        string
	// RouteKeys are chat bindings created via the connector's admin routes
	// API (chat_routes). Provision's own routeKeys field feeds a different
	// table (the per-gateway routing row) — both are wired on create.
	RouteKeys []RouteKey
	// Connector requests provisioning even with no routes yet.
	Connector bool
}

// Manager wires the Kubernetes and connector clients together.
type Manager struct {
	K8s       k8s.Client
	Connector connector.Client
	// Agent polls session pods' /api/status for Idle v2. May be nil
	// (treated as disabled) so existing constructions stay valid.
	Agent    agent.Client
	Defaults Defaults
	Log      *slog.Logger

	mu          sync.Mutex
	instances   map[string]connector.Instance // by gatewayId
	instancesAt time.Time
}

// AgentClient returns the status poller, never nil.
func (m *Manager) AgentClient() agent.Client {
	if m.Agent == nil {
		return agent.Disabled{}
	}
	return m.Agent
}

// Create makes the claim and, when requested, provisions the connector
// instance. On provision failure the claim is rolled back — no half-session
// survives.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (*session.Session, error) {
	id := req.ID
	if id == "" {
		id = session.NewID()
	} else if err := session.ValidateID(id); err != nil {
		return nil, err
	}
	useConnector := req.Connector || len(req.RouteKeys) > 0
	if useConnector && !m.Connector.Enabled() {
		return nil, ErrConnectorDisabled
	}

	annotations := map[string]string{}
	if req.TTLSeconds != nil {
		annotations[session.AnnoTTLSeconds] = strconv.FormatInt(*req.TTLSeconds, 10)
	}
	if req.IdleTimeoutSeconds != nil {
		annotations[session.AnnoIdleTimeoutSeconds] = strconv.FormatInt(*req.IdleTimeoutSeconds, 10)
	}
	if req.DisplayName != "" {
		annotations[session.AnnoDisplayName] = req.DisplayName
	}
	annotations[session.AnnoConnector] = strconv.FormatBool(useConnector)

	warmPool := req.WarmPool
	if warmPool == "" {
		warmPool = m.Defaults.WarmPool
	}
	if err := m.K8s.CreateClaim(ctx, k8s.ClaimSpec{Name: id, WarmPool: warmPool, Annotations: annotations}); err != nil {
		return nil, err
	}

	if useConnector {
		chatIDs := make([]string, 0, len(req.RouteKeys))
		for _, rk := range req.RouteKeys {
			chatIDs = append(chatIDs, rk.ChatID)
		}
		_, err := m.Connector.Provision(ctx, connector.ProvisionRequest{
			GatewayID:   id,
			Platform:    m.Defaults.Platform,
			BotID:       m.Defaults.BotID,
			RouteKeys:   chatIDs,
			WakeURL:     m.Defaults.WakeBaseURL + "/wake/" + id,
			DisplayName: req.DisplayName,
		})
		if err == nil {
			// Chat bindings live in a separate table from provision's
			// routing row; each one is an explicit admin-API call.
			for _, rk := range req.RouteKeys {
				platform := rk.Platform
				if platform == "" {
					platform = m.Defaults.Platform
				}
				if err = m.Connector.SetRoute(ctx, connector.Route{
					Platform: platform, ChatID: rk.ChatID, GatewayID: id,
				}); err != nil {
					err = fmt.Errorf("binding chat %s/%s: %w", platform, rk.ChatID, err)
					break
				}
			}
		}
		if err != nil {
			m.Log.Error("provision failed; rolling back", "session", id, "error", err)
			if delErr := m.Connector.DeleteInstance(ctx, id); delErr != nil && !errors.Is(delErr, connector.ErrNotFound) {
				m.Log.Warn("rollback deprovision failed", "session", id, "error", delErr)
			}
			if delErr := m.K8s.DeleteClaim(ctx, id); delErr != nil && !errors.Is(delErr, k8s.ErrNotFound) {
				m.Log.Error("rollback delete failed; claim orphaned", "session", id, "error", delErr)
			}
			return nil, fmt.Errorf("connector provision failed: %w", err)
		}
		m.invalidateInstances()
	}

	return m.Get(ctx, id)
}

// Get assembles the merged session view.
func (m *Manager) Get(ctx context.Context, id string) (*session.Session, error) {
	claim, err := m.K8s.GetClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.assemble(ctx, claim), nil
}

// List assembles all managed sessions.
func (m *Manager) List(ctx context.Context) ([]session.Session, error) {
	claims, err := m.K8s.ListClaims(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]session.Session, 0, len(claims))
	for i := range claims {
		sessions = append(sessions, *m.assemble(ctx, &claims[i]))
	}
	return sessions, nil
}

// Wake unconditionally sets the session's sandbox to Running —
// idempotent, safe mid-suspension.
func (m *Manager) Wake(ctx context.Context, id string) error {
	claim, err := m.K8s.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	sandboxName := claim.SandboxName
	if sandboxName == "" {
		sandboxName = claim.Name
	}
	if err := m.K8s.PatchSandboxOperatingMode(ctx, sandboxName, "Running"); err != nil {
		return err
	}
	// Any wake satisfies a pending scheduled wake; clear it so the sweeper
	// doesn't re-fire a stale one after the next suspend. Best-effort: a
	// leftover annotation only causes a redundant (idempotent) wake.
	if claim.Annotations[session.AnnoWakeAt] != "" {
		if err := m.K8s.PatchClaimAnnotations(ctx, claim.Name, map[string]*string{session.AnnoWakeAt: nil}); err != nil {
			m.Log.Warn("wake-at clear failed", "session", claim.Name, "error", err)
		}
	}
	return nil
}

// Restart cycles the session's pod without touching the claim or its PVC:
// a suspend immediately followed by a wake. The agent-sandbox controller
// tears the pod down and recreates it fresh from the current template
// (env, image — whatever's live now), re-running boot-time self-provision;
// the home directory PVC reattaches untouched. Errors from the wake half
// are returned; a failed suspend half means the sandbox was already not
// running, which wake alone still recovers from (idempotent).
func (m *Manager) Restart(ctx context.Context, id string) error {
	claim, err := m.K8s.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	sandboxName := claim.SandboxName
	if sandboxName == "" {
		sandboxName = claim.Name
	}
	_ = m.K8s.PatchSandboxOperatingMode(ctx, sandboxName, "Suspended")
	return m.K8s.PatchSandboxOperatingMode(ctx, sandboxName, "Running")
}

// Decommission is the only session-destruction code path (used by both
// DELETE /v1/sessions/{id} and the TTL sweeper). Connector purge runs first;
// if it fails for any reason other than "already gone", the claim is kept so
// the next sweep (or a redelete) retries — claim existence is the retry state.
func (m *Manager) Decommission(ctx context.Context, id string) (deprovisioned bool, err error) {
	claim, err := m.K8s.GetClaim(ctx, id)
	if err != nil {
		return false, err
	}
	if claim.Annotations[session.AnnoConnector] == "true" && m.Connector.Enabled() {
		err := m.Connector.DeleteInstance(ctx, id)
		switch {
		case err == nil:
			deprovisioned = true
			m.invalidateInstances()
		case errors.Is(err, connector.ErrNotFound):
			// Already purged — fine.
		default:
			return false, fmt.Errorf("connector deprovision failed (claim retained for retry): %w", err)
		}
	}
	if err := m.K8s.DeleteClaim(ctx, id); err != nil && !errors.Is(err, k8s.ErrNotFound) {
		return deprovisioned, err
	}
	m.Log.Info("session decommissioned", "session", id, "deprovisioned", deprovisioned)
	return deprovisioned, nil
}

// EffectiveTTL returns the session's TTL (annotation override else default).
func (m *Manager) EffectiveTTL(claim *k8s.Claim) time.Duration {
	return effectiveDuration(claim.Annotations[session.AnnoTTLSeconds], m.Defaults.TTL)
}

// EffectiveIdleTimeout returns the session's idle timeout.
func (m *Manager) EffectiveIdleTimeout(claim *k8s.Claim) time.Duration {
	return effectiveDuration(claim.Annotations[session.AnnoIdleTimeoutSeconds], m.Defaults.IdleTimeout)
}

func effectiveDuration(annotation string, def time.Duration) time.Duration {
	if annotation == "" {
		return def
	}
	secs, err := strconv.ParseInt(annotation, 10, 64)
	if err != nil || secs < 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}

// Sandbox resolves a claim's bound sandbox, tolerating not-yet-bound.
func (m *Manager) Sandbox(ctx context.Context, claim *k8s.Claim) *k8s.Sandbox {
	name := claim.SandboxName
	if name == "" {
		name = claim.Name
	}
	sandbox, err := m.K8s.GetSandbox(ctx, name)
	if err != nil {
		if !errors.Is(err, k8s.ErrNotFound) {
			m.Log.Warn("sandbox read failed", "session", claim.Name, "sandbox", name, "error", err)
		}
		return nil
	}
	return sandbox
}

// Instances returns the connector instance map, cached briefly so listing N
// sessions costs one admin call, not N. Returns nil when disabled or when
// the connector is unreachable/throttled (callers treat nil as "unknown").
func (m *Manager) Instances(ctx context.Context) map[string]connector.Instance {
	if !m.Connector.Enabled() {
		return nil
	}
	m.mu.Lock()
	if m.instances != nil && time.Since(m.instancesAt) < instanceCacheTTL {
		defer m.mu.Unlock()
		return m.instances
	}
	m.mu.Unlock()

	list, err := m.Connector.ListInstances(ctx)
	if err != nil {
		if !errors.Is(err, connector.ErrThrottled) {
			m.Log.Warn("instance list failed", "error", err)
		}
		return nil
	}
	byID := make(map[string]connector.Instance, len(list))
	for _, inst := range list {
		byID[inst.GatewayID] = inst
	}
	m.mu.Lock()
	m.instances, m.instancesAt = byID, time.Now()
	m.mu.Unlock()
	return byID
}

func (m *Manager) invalidateInstances() {
	m.mu.Lock()
	m.instances, m.instancesAt = nil, time.Time{}
	m.mu.Unlock()
}

// assemble merges claim + sandbox + connector instance into the API view.
func (m *Manager) assemble(ctx context.Context, claim *k8s.Claim) *session.Session {
	sandbox := m.Sandbox(ctx, claim)
	facts := session.Facts{Terminating: claim.Terminating, HasSandbox: sandbox != nil}
	s := &session.Session{
		ID:                 claim.Name,
		CreatedAt:          claim.CreatedAt,
		TTLSeconds:         int64(m.EffectiveTTL(claim) / time.Second),
		IdleTimeoutSeconds: int64(m.EffectiveIdleTimeout(claim) / time.Second),
		DisplayName:        claim.Annotations[session.AnnoDisplayName],
	}
	if sandbox != nil {
		facts.OperatingMode = sandbox.OperatingMode
		facts.Ready = sandbox.Ready
		facts.Suspended = sandbox.Suspended
		s.OperatingMode = sandbox.OperatingMode
		if s.OperatingMode == "" {
			s.OperatingMode = "Running"
		}
	}
	s.State = session.Derive(facts)

	if claim.Annotations[session.AnnoConnector] == "true" && m.Connector.Enabled() {
		view := &session.ConnectorView{}
		if instances := m.Instances(ctx); instances != nil {
			if inst, ok := instances[claim.Name]; ok {
				view.Provisioned = true
				view.Connected = inst.Connected
				view.Revoked = inst.Revoked
				view.BufferedCount = inst.BufferedCount
				view.LastInboundAt = inst.LastInboundAt
				view.TurnInFlight = inst.TurnInFlight
			}
		}
		s.Connector = view
	}
	return s
}

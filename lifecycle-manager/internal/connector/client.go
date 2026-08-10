// Package connector is the optional hermes-relay-connector integration: a
// typed client for the connector's admin plane and provision endpoint,
// mirroring the contract verified against the connector source (all JSON
// camelCase, timestamps as unix-second integers, errors as {"error": "..."}).
package connector

import (
	"context"
	"errors"
)

// ErrDisabled is returned by the noop client wired in when the integration
// is off (HLM_CONNECTOR_ENABLED=false).
var ErrDisabled = errors.New("connector integration disabled")

// ErrThrottled short-circuits calls while the connector's per-source-IP auth
// throttle window is assumed open; callers skip work, never retry.
var ErrThrottled = errors.New("connector throttled (429); backing off")

// ErrNotFound maps the connector's 404s.
var ErrNotFound = errors.New("not found")

// APIError is any other non-2xx admin response.
type APIError struct {
	Status  int
	Message string // the {"error": ...} body when present
}

func (e *APIError) Error() string {
	return "connector: HTTP " + itoa(e.Status) + ": " + e.Message
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Instance is one row of GET /admin/v1/instances.
type Instance struct {
	GatewayID     string  `json:"gatewayId"`
	Tenant        string  `json:"tenant"`
	InstanceID    *string `json:"instanceId"`
	WakeURL       *string `json:"wakeUrl"`
	DisplayName   *string `json:"displayName"`
	Revoked       bool    `json:"revoked"`
	Connected     bool    `json:"connected"`
	BufferedCount int     `json:"bufferedCount"`
	// LastInboundAt / LastOutboundAt are unix seconds and in-memory on the
	// connector side: nil after a connector restart means "unknown", which
	// consumers must never read as "idle".
	LastInboundAt  *int64 `json:"lastInboundAt"`
	LastOutboundAt *int64 `json:"lastOutboundAt"`
	TurnInFlight   bool   `json:"turnInFlight"`
}

// ProvisionRequest is the body of POST /relay/provision.
type ProvisionRequest struct {
	GatewayID   string   `json:"gatewayId"`
	Platform    string   `json:"platform"`
	BotID       string   `json:"botId"`
	RouteKeys   []string `json:"routeKeys,omitempty"`
	InstanceID  string   `json:"instanceId,omitempty"`
	WakeURL     string   `json:"wakeUrl,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
}

// ProvisionResult reports what provision did. The gateway secret and delivery
// key in the connector's response are read into no field anywhere: the agent
// pod re-provisions on boot and rotates them; the lifecycle-manager must
// never hold or log them.
type ProvisionResult struct {
	Tenant    string
	GatewayID string
	RouteKeys []string
}

// InstancePatch is the body of PATCH /admin/v1/instances/{id}. Nil fields are
// omitted (COALESCE on the connector side: nulls never clear stored values).
type InstancePatch struct {
	WakeURL     *string `json:"wakeUrl,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
}

// Route is one Discord-chat → gateway binding.
type Route struct {
	Platform  string `json:"platform"`
	ChatID    string `json:"chatId"`
	GatewayID string `json:"gatewayId"`
}

// Client is the connector surface the lifecycle-manager consumes.
type Client interface {
	Enabled() bool
	Provision(ctx context.Context, req ProvisionRequest) (*ProvisionResult, error)
	ListInstances(ctx context.Context) ([]Instance, error)
	PatchInstance(ctx context.Context, gatewayID string, patch InstancePatch) error
	Revoke(ctx context.Context, gatewayID string) error
	// DeleteInstance is decommission's deprovision: closes live sockets and
	// purges buffer, routes, policies, and the gateway row.
	DeleteInstance(ctx context.Context, gatewayID string) error
	SetRoute(ctx context.Context, route Route) error
	ListRoutes(ctx context.Context, gatewayID string) ([]Route, error)
	DeleteRoute(ctx context.Context, platform, chatID string) error
}

// Disabled is the no-op Client used when the integration is off.
type Disabled struct{}

func (Disabled) Enabled() bool { return false }
func (Disabled) Provision(context.Context, ProvisionRequest) (*ProvisionResult, error) {
	return nil, ErrDisabled
}
func (Disabled) ListInstances(context.Context) ([]Instance, error)          { return nil, ErrDisabled }
func (Disabled) PatchInstance(context.Context, string, InstancePatch) error { return ErrDisabled }
func (Disabled) Revoke(context.Context, string) error                       { return ErrDisabled }
func (Disabled) DeleteInstance(context.Context, string) error               { return ErrDisabled }
func (Disabled) SetRoute(context.Context, Route) error                      { return ErrDisabled }
func (Disabled) ListRoutes(context.Context, string) ([]Route, error)        { return nil, ErrDisabled }
func (Disabled) DeleteRoute(context.Context, string, string) error          { return ErrDisabled }

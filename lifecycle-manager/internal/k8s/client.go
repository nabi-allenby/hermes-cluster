// Package k8s is the lifecycle-manager's minimal Kubernetes surface: CRUD on
// SandboxClaims and read/patch on Sandboxes, via the dynamic client. All
// agent-sandbox field-path knowledge lives in unstructured.go.
package k8s

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a claim or sandbox does not exist.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists is returned by CreateClaim on name collision.
var ErrAlreadyExists = errors.New("already exists")

// Claim is the lifecycle-manager's view of a SandboxClaim.
type Claim struct {
	Name        string
	UID         string // metadata.uid — ownerReference target for exposure objects
	CreatedAt   time.Time
	Annotations map[string]string
	Terminating bool
	// SandboxName is status.sandbox.name — on warm-pool adoption it differs
	// from the claim name. Empty until the controller binds a sandbox.
	SandboxName string
}

// Sandbox is the lifecycle-manager's view of a Sandbox.
type Sandbox struct {
	Name          string
	OperatingMode string // "Running" | "Suspended" | "" (defaults Running)
	Ready         bool
	ReadyChanged  time.Time // Ready condition's lastTransitionTime
	Suspended     bool
	// ServiceFQDN is status.serviceFQDN. agent-sandbox v0.5.4 never
	// populates it (issue #11) — kept for forward compatibility; the
	// status-poll target falls back to the exposure Service.
	ServiceFQDN string
	// Selector is status.selector — the pod label selector the controller
	// publishes (e.g. "agents.x-k8s.io/sandbox-name-hash=ab93ad90"). The
	// exposure Service selects on it; empty until the sandbox is bound.
	Selector string
}

// ClaimSpec is the input to CreateClaim.
type ClaimSpec struct {
	Name        string
	WarmPool    string
	Annotations map[string]string
}

// ExposureSpec describes the in-cluster surface fronting one session's
// serve container: a Service always, plus an Ingress when Host is set.
// Both carry an ownerReference to the SandboxClaim, so Kubernetes GC
// removes them with the claim — there is no separate cleanup path.
type ExposureSpec struct {
	SessionName string // Service/Ingress name == claim name
	OwnerUID    string // claim metadata.uid for the ownerReference
	Selector    string // sandbox status.selector ("key=value")
	Port        int    // serve container port the Service targets

	// Host, when non-empty, adds an Ingress for this hostname, TLS from
	// TLSSecret (the pre-issued wildcard). DenyService receives
	// /auth/password-login: a no-endpoints Service, so the edge answers
	// 503 there — never 401, which makes Hermes Desktop drop its tokens
	// and force an interactive re-login (PLAN §11).
	Host         string
	IngressClass string
	TLSSecret    string
	DenyService  string
}

// Client is everything the lifecycle-manager does against Kubernetes.
type Client interface {
	CreateClaim(ctx context.Context, spec ClaimSpec) error
	GetClaim(ctx context.Context, name string) (*Claim, error)
	// ListClaims returns managed claims only (ManagedLabel selector).
	ListClaims(ctx context.Context) ([]Claim, error)
	DeleteClaim(ctx context.Context, name string) error
	// PatchClaimAnnotations merge-patches claim annotations: a nil value
	// removes the key. Used for the scheduled-wake annotation (wake-at).
	PatchClaimAnnotations(ctx context.Context, name string, annotations map[string]*string) error
	GetSandbox(ctx context.Context, name string) (*Sandbox, error)
	PatchSandboxOperatingMode(ctx context.Context, name, mode string) error
	// EnsureExposure creates the session's Service (and Ingress when
	// spec.Host is set) if missing; existing objects are left untouched.
	// Idempotent-by-create: the selector is deterministic per session
	// (FNV of the sandbox name == claim name while warm pools are 0), so
	// pod churn never invalidates an existing Service.
	EnsureExposure(ctx context.Context, spec ExposureSpec) error
	// Ping verifies API-server reachability (used by /readyz).
	Ping(ctx context.Context) error
}

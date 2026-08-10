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
}

// ClaimSpec is the input to CreateClaim.
type ClaimSpec struct {
	Name        string
	WarmPool    string
	Annotations map[string]string
}

// Client is everything the lifecycle-manager does against Kubernetes.
type Client interface {
	CreateClaim(ctx context.Context, spec ClaimSpec) error
	GetClaim(ctx context.Context, name string) (*Claim, error)
	// ListClaims returns managed claims only (ManagedLabel selector).
	ListClaims(ctx context.Context) ([]Claim, error)
	DeleteClaim(ctx context.Context, name string) error
	GetSandbox(ctx context.Context, name string) (*Sandbox, error)
	PatchSandboxOperatingMode(ctx context.Context, name, mode string) error
	// Ping verifies API-server reachability (used by /readyz).
	Ping(ctx context.Context) error
}

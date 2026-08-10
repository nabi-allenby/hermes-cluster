// Package session defines the session model: identity rules, the annotation
// vocabulary stored on SandboxClaims, and the derived state machine.
//
// A session is one SandboxClaim (= one Sandbox = one PVC = one connector
// gatewayId). The lifecycle-manager itself is stateless: everything it knows
// about a session lives on the claim as labels/annotations, or is derived
// live from the Sandbox and the connector.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Label and annotation keys stamped on managed SandboxClaims.
const (
	// ManagedLabel marks claims owned by the lifecycle-manager. Sweepers and
	// list endpoints never touch claims without it.
	ManagedLabel = "hermes.nabi.dev/managed"

	AnnoTTLSeconds         = "hermes.nabi.dev/ttl-seconds"
	AnnoIdleTimeoutSeconds = "hermes.nabi.dev/idle-timeout-seconds"
	AnnoConnector          = "hermes.nabi.dev/connector"
	AnnoDisplayName        = "hermes.nabi.dev/display-name"
)

// idPattern is a DNS-1123 label: the id doubles as the claim name and the
// connector gatewayId, so the strictest consumer (Kubernetes) wins.
var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,50}[a-z0-9])?$`)

// ErrInvalidID marks session-id validation failures (HTTP 400).
var ErrInvalidID = errors.New("invalid session id")

// ValidateID reports whether id is usable as a session id.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w %q: must be a DNS-1123 label (lowercase alphanumerics and '-', max 52 chars)", ErrInvalidID, id)
	}
	return nil
}

// NewID generates a random session id of the form "s-<10 hex>".
func NewID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not a recoverable condition
	}
	return "s-" + hex.EncodeToString(b)
}

// Session is the merged external view of one session, assembled from the
// claim, its sandbox, and (optionally) the connector instance.
type Session struct {
	ID                 string         `json:"id"`
	State              State          `json:"state"`
	OperatingMode      string         `json:"operatingMode,omitempty"`
	CreatedAt          time.Time      `json:"createdAt"`
	TTLSeconds         int64          `json:"ttlSeconds"`
	IdleTimeoutSeconds int64          `json:"idleTimeoutSeconds"`
	DisplayName        string         `json:"displayName,omitempty"`
	Connector          *ConnectorView `json:"connector,omitempty"`
}

// ConnectorView is the connector-sourced slice of a Session, present only
// when the integration is enabled and the session was provisioned.
type ConnectorView struct {
	Provisioned   bool   `json:"provisioned"`
	Connected     bool   `json:"connected"`
	Revoked       bool   `json:"revoked"`
	BufferedCount int    `json:"bufferedCount"`
	LastInboundAt *int64 `json:"lastInboundAt"`
	TurnInFlight  bool   `json:"turnInFlight"`
}

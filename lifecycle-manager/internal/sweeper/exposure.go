package sweeper

// Exposure: the level-triggered half of the dashboard feature (PLAN T4).
// Each sweep ensures every bound session has its Service (the Idle v2
// status-poll target — the fix for issue #11) and, for Discord-provisioned
// sessions on a configured domain, its Ingress. Deletion is not handled
// here: both objects carry an ownerReference to the SandboxClaim and
// Kubernetes GC removes them with it.

import (
	"context"
	"regexp"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
)

// dashboardIDPattern gates Ingress creation to Discord-provisioned
// sessions (s-dc-<snowflake>). Only these map to a real person the broker
// can authorize (the snowflake is the provisioning key and the join to the
// broker's user); API-created sessions (s-<random>) are operator artifacts
// with no owner, so they get no public hostname — Service-only. The broker
// is single-app now (Auth0), so the gate is a deliberate exposure policy,
// not a per-app-registration constraint.
var dashboardIDPattern = regexp.MustCompile(`^s-dc-[0-9]+$`)

// ExposureConfig is the sweeper's exposure policy. Nil disables everything.
type ExposureConfig struct {
	Port int // serve container port (Service target, Ingress backend)

	// Domain, when non-empty, enables per-session Ingresses at
	// <session>.<Domain> for dashboard-eligible sessions.
	Domain       string
	IngressClass string
	TLSSecret    string
	DenyService  string
}

// sweepExposure ensures one session's exposure objects exist. Requires the
// sandbox to be bound with a published selector; earlier sweeps simply
// retry (the ticker is the level trigger).
func (r *Runner) sweepExposure(ctx context.Context, claim *k8s.Claim, sb *k8s.Sandbox) {
	if r.Exposure == nil {
		return
	}
	if sb == nil || sb.Selector == "" {
		return // not bound yet; next sweep
	}
	spec := k8s.ExposureSpec{
		SessionName: claim.Name,
		OwnerUID:    claim.UID,
		Selector:    sb.Selector,
		Port:        r.Exposure.Port,
	}
	if r.Exposure.Domain != "" && dashboardIDPattern.MatchString(claim.Name) {
		spec.Host = claim.Name + "." + r.Exposure.Domain
		spec.IngressClass = r.Exposure.IngressClass
		spec.TLSSecret = r.Exposure.TLSSecret
		spec.DenyService = r.Exposure.DenyService
	}
	if err := r.Manager.K8s.EnsureExposure(ctx, spec); err != nil {
		r.Log.Error("exposure ensure failed; will retry next sweep", "session", claim.Name, "error", err)
	}
}

// statusTarget picks the address the Idle v2 pollers dial: the controller's
// serviceFQDN when published (forward compatibility), else the exposure
// Service — which shares the claim's name and namespace. Empty means no
// target exists yet (sandbox unbound and exposure disabled), and pollers
// fail closed as before.
func (r *Runner) statusTarget(claim *k8s.Claim, sb *k8s.Sandbox) string {
	if sb != nil && sb.ServiceFQDN != "" {
		return sb.ServiceFQDN
	}
	if r.Exposure != nil {
		return claim.Name
	}
	return ""
}

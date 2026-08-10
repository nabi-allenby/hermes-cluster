package k8s

// Every agent-sandbox CRD field path the lifecycle-manager relies on is in
// this file and nowhere else, so a pin bump that moves the schema is a
// one-file change. Facts verified against v0.5.4 — see docs/p-m0.md.

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	kindSandboxClaim = "SandboxClaim"

	resourceSandboxClaims = "sandboxclaims"
	resourceSandboxes     = "sandboxes"

	conditionReady     = "Ready"
	conditionSuspended = "Suspended"
)

// newClaimObject builds a v1beta1 SandboxClaim (spec.warmPoolRef is the only
// spec field we set; per-session knobs travel as annotations).
func newClaimObject(apiVersion, namespace string, spec ClaimSpec, managedLabel string) *unstructured.Unstructured {
	annotations := map[string]interface{}{}
	for k, v := range spec.Annotations {
		annotations[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kindSandboxClaim,
		"metadata": map[string]interface{}{
			"name":        spec.Name,
			"namespace":   namespace,
			"labels":      map[string]interface{}{managedLabel: "true"},
			"annotations": annotations,
		},
		"spec": map[string]interface{}{
			"warmPoolRef": map[string]interface{}{"name": spec.WarmPool},
		},
	}}
}

// claimFromUnstructured extracts the fields the lifecycle-manager reads.
func claimFromUnstructured(u *unstructured.Unstructured) (*Claim, error) {
	c := &Claim{
		Name:        u.GetName(),
		CreatedAt:   u.GetCreationTimestamp().Time,
		Annotations: u.GetAnnotations(),
		Terminating: u.GetDeletionTimestamp() != nil,
	}
	name, found, err := unstructured.NestedString(u.Object, "status", "sandbox", "name")
	if err != nil {
		return nil, fmt.Errorf("claim %s: status.sandbox.name: %w", c.Name, err)
	}
	if found {
		c.SandboxName = name
	}
	return c, nil
}

// sandboxFromUnstructured extracts operatingMode and the two conditions.
func sandboxFromUnstructured(u *unstructured.Unstructured) (*Sandbox, error) {
	s := &Sandbox{Name: u.GetName()}
	mode, _, err := unstructured.NestedString(u.Object, "spec", "operatingMode")
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: spec.operatingMode: %w", s.Name, err)
	}
	s.OperatingMode = mode

	conditions, _, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: status.conditions: %w", s.Name, err)
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		condStatus, _ := cond["status"].(string)
		switch condType {
		case conditionReady:
			s.Ready = condStatus == "True"
			if ts, ok := cond["lastTransitionTime"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					s.ReadyChanged = t
				}
			}
		case conditionSuspended:
			s.Suspended = condStatus == "True"
		}
	}
	return s, nil
}

// operatingModePatch is the JSON merge patch flipping a sandbox's mode.
func operatingModePatch(mode string) []byte {
	return []byte(fmt.Sprintf(`{"spec":{"operatingMode":%q}}`, mode))
}

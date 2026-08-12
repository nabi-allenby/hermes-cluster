package k8s

// Exposure objects: the per-session Service (status-poll target + Ingress
// backend) and the per-session Ingress (dashboard hostname). Created by the
// sweeper's level-triggered ensure; deleted by Kubernetes GC via the
// ownerReference to the SandboxClaim — never by lifecycle-manager code.
//
// agent-sandbox v0.5.4 publishes status.selector but creates no Service and
// never fills status.serviceFQDN (issue #11); these Services are what make
// Idle v2 status polling reach pods at all.

import (
	"context"
	"fmt"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	resourceServices  = "services"
	resourceIngresses = "ingresses"

	// pathPasswordLogin is the one upstream route that must not be
	// internet-reachable: it bypasses the IdP's assignment check with the
	// shared basic credential (PLAN T3). Routed to the deny Service.
	pathPasswordLogin = "/auth/password-login"
)

// parseSelector splits a label-selector string ("k=v,k2=v2") into the map
// a Service spec.selector wants. Unparseable pairs are skipped.
func parseSelector(s string) map[string]interface{} {
	out := map[string]interface{}{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			out[k] = v
		}
	}
	return out
}

func (c *DynamicClient) EnsureExposure(ctx context.Context, spec ExposureSpec) error {
	if spec.Selector == "" {
		return fmt.Errorf("exposure %q: empty selector", spec.SessionName)
	}
	owner := map[string]interface{}{
		"apiVersion": c.claimAPIV,
		"kind":       kindSandboxClaim,
		"name":       spec.SessionName,
		"uid":        spec.OwnerUID,
	}

	svc := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]interface{}{
			"name":            spec.SessionName,
			"namespace":       c.namespace,
			"ownerReferences": []interface{}{owner},
		},
		"spec": map[string]interface{}{
			"selector": parseSelector(spec.Selector),
			"ports": []interface{}{map[string]interface{}{
				"port":       int64(spec.Port),
				"targetPort": int64(spec.Port),
			}},
		},
	}}
	_, err := c.dyn.Resource(c.svcGVR).Namespace(c.namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("exposure %q: service: %w", spec.SessionName, err)
	}

	if spec.Host == "" {
		return nil
	}
	prefix := "Prefix"
	ing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata": map[string]interface{}{
			"name":            spec.SessionName,
			"namespace":       c.namespace,
			"ownerReferences": []interface{}{owner},
		},
		"spec": map[string]interface{}{
			"ingressClassName": spec.IngressClass,
			"tls": []interface{}{map[string]interface{}{
				"hosts":      []interface{}{spec.Host},
				"secretName": spec.TLSSecret,
			}},
			"rules": []interface{}{map[string]interface{}{
				"host": spec.Host,
				"http": map[string]interface{}{
					"paths": []interface{}{
						// Order matters to readers, not the controller:
						// nginx matches the longest path first.
						map[string]interface{}{
							"path":     pathPasswordLogin,
							"pathType": prefix,
							"backend": map[string]interface{}{
								"service": map[string]interface{}{
									"name": spec.DenyService,
									"port": map[string]interface{}{"number": int64(spec.Port)},
								},
							},
						},
						map[string]interface{}{
							"path":     "/",
							"pathType": prefix,
							"backend": map[string]interface{}{
								"service": map[string]interface{}{
									"name": spec.SessionName,
									"port": map[string]interface{}{"number": int64(spec.Port)},
								},
							},
						},
					},
				},
			}},
		},
	}}
	_, err = c.dyn.Resource(c.ingGVR).Namespace(c.namespace).Create(ctx, ing, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("exposure %q: ingress: %w", spec.SessionName, err)
	}
	return nil
}

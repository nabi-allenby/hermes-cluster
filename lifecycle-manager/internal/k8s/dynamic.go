package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

// Options configure the dynamic client.
type Options struct {
	Namespace string
	Group     string // Sandbox group (agents.x-k8s.io)
	ExtGroup  string // claim/template/warmpool group (extensions.agents.x-k8s.io)
	Version   string
}

// DynamicClient implements Client over the Kubernetes dynamic client.
type DynamicClient struct {
	dyn       dynamic.Interface
	namespace string
	claimGVR  schema.GroupVersionResource
	sboxGVR   schema.GroupVersionResource
	claimAPIV string
}

// NewDynamicClient connects in-cluster, falling back to KUBECONFIG /
// ~/.kube/config for local development.
func NewDynamicClient(opts Options) (*DynamicClient, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, herr := os.UserHomeDir()
			if herr != nil {
				return nil, fmt.Errorf("no in-cluster config and no home dir: %w", err)
			}
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("neither in-cluster nor kubeconfig configuration available: %w", err)
		}
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return newWithInterface(dyn, opts), nil
}

func newWithInterface(dyn dynamic.Interface, opts Options) *DynamicClient {
	return &DynamicClient{
		dyn:       dyn,
		namespace: opts.Namespace,
		claimGVR:  schema.GroupVersionResource{Group: opts.ExtGroup, Version: opts.Version, Resource: resourceSandboxClaims},
		sboxGVR:   schema.GroupVersionResource{Group: opts.Group, Version: opts.Version, Resource: resourceSandboxes},
		claimAPIV: opts.ExtGroup + "/" + opts.Version,
	}
}

func (c *DynamicClient) CreateClaim(ctx context.Context, spec ClaimSpec) error {
	obj := newClaimObject(c.claimAPIV, c.namespace, spec, session.ManagedLabel)
	_, err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).Create(ctx, obj, metav1.CreateOptions{})
	if k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("claim %q: %w", spec.Name, ErrAlreadyExists)
	}
	return err
}

func (c *DynamicClient) GetClaim(ctx context.Context, name string) (*Claim, error) {
	u, err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	claim, err := claimFromUnstructured(u)
	if err != nil {
		return nil, err
	}
	if claim.Annotations[session.ManagedLabel] == "" && u.GetLabels()[session.ManagedLabel] != "true" {
		// Unmanaged claims are invisible to the lifecycle-manager.
		return nil, fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	return claim, nil
}

func (c *DynamicClient) ListClaims(ctx context.Context) ([]Claim, error) {
	list, err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: session.ManagedLabel + "=true",
	})
	if err != nil {
		return nil, err
	}
	claims := make([]Claim, 0, len(list.Items))
	for i := range list.Items {
		claim, err := claimFromUnstructured(&list.Items[i])
		if err != nil {
			return nil, err
		}
		claims = append(claims, *claim)
	}
	return claims, nil
}

func (c *DynamicClient) DeleteClaim(ctx context.Context, name string) error {
	err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	return err
}

func (c *DynamicClient) PatchClaimAnnotations(ctx context.Context, name string, annotations map[string]*string) error {
	_, err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).Patch(
		ctx, name, types.MergePatchType, annotationsPatch(annotations), metav1.PatchOptions{})
	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	return err
}

func (c *DynamicClient) GetSandbox(ctx context.Context, name string) (*Sandbox, error) {
	u, err := c.dyn.Resource(c.sboxGVR).Namespace(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("sandbox %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return sandboxFromUnstructured(u)
}

func (c *DynamicClient) PatchSandboxOperatingMode(ctx context.Context, name, mode string) error {
	_, err := c.dyn.Resource(c.sboxGVR).Namespace(c.namespace).Patch(
		ctx, name, types.MergePatchType, operatingModePatch(mode), metav1.PatchOptions{})
	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("sandbox %q: %w", name, ErrNotFound)
	}
	return err
}

func (c *DynamicClient) Ping(ctx context.Context) error {
	_, err := c.dyn.Resource(c.claimGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

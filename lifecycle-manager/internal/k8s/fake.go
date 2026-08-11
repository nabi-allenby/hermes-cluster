package k8s

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Fake is an in-memory Client for unit tests. The zero value is usable.
type Fake struct {
	mu        sync.Mutex
	claims    map[string]*Claim
	sandboxes map[string]*Sandbox
	// CreateSandboxOnClaim makes CreateClaim immediately bind a Ready
	// sandbox of the same name, mimicking a settled controller.
	CreateSandboxOnClaim bool
	// Err, when set, is returned by every method (API-server outage).
	Err error
}

func (f *Fake) init() {
	if f.claims == nil {
		f.claims = map[string]*Claim{}
	}
	if f.sandboxes == nil {
		f.sandboxes = map[string]*Sandbox{}
	}
}

// AddSession seeds a claim + bound sandbox.
func (f *Fake) AddSession(name string, createdAt time.Time, annotations map[string]string, mode string, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if annotations == nil {
		annotations = map[string]string{}
	}
	f.claims[name] = &Claim{Name: name, CreatedAt: createdAt, Annotations: annotations, SandboxName: name}
	f.sandboxes[name] = &Sandbox{
		Name:          name,
		OperatingMode: mode,
		Ready:         ready,
		ReadyChanged:  createdAt,
		Suspended:     mode == "Suspended",
		// Mimic a bound Service so status-polling tests have a target.
		ServiceFQDN: name,
	}
}

func (f *Fake) SetSandbox(name string, s Sandbox) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.sandboxes[name] = &s
}

func (f *Fake) CreateClaim(_ context.Context, spec ClaimSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return f.Err
	}
	if _, ok := f.claims[spec.Name]; ok {
		return fmt.Errorf("claim %q: %w", spec.Name, ErrAlreadyExists)
	}
	annotations := map[string]string{}
	for k, v := range spec.Annotations {
		annotations[k] = v
	}
	claim := &Claim{Name: spec.Name, CreatedAt: time.Now(), Annotations: annotations}
	if f.CreateSandboxOnClaim {
		claim.SandboxName = spec.Name
		f.sandboxes[spec.Name] = &Sandbox{Name: spec.Name, OperatingMode: "Running", Ready: true, ReadyChanged: claim.CreatedAt}
	}
	f.claims[spec.Name] = claim
	return nil
}

func (f *Fake) GetClaim(_ context.Context, name string) (*Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return nil, f.Err
	}
	c, ok := f.claims[name]
	if !ok {
		return nil, fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	cp := *c
	return &cp, nil
}

func (f *Fake) ListClaims(context.Context) ([]Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return nil, f.Err
	}
	names := make([]string, 0, len(f.claims))
	for n := range f.claims {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Claim, 0, len(names))
	for _, n := range names {
		out = append(out, *f.claims[n])
	}
	return out, nil
}

func (f *Fake) DeleteClaim(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return f.Err
	}
	c, ok := f.claims[name]
	if !ok {
		return fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	delete(f.claims, name)
	if c.SandboxName != "" {
		delete(f.sandboxes, c.SandboxName)
	}
	return nil
}

func (f *Fake) PatchClaimAnnotations(_ context.Context, name string, annotations map[string]*string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return f.Err
	}
	c, ok := f.claims[name]
	if !ok {
		return fmt.Errorf("claim %q: %w", name, ErrNotFound)
	}
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		if v == nil {
			delete(c.Annotations, k)
		} else {
			c.Annotations[k] = *v
		}
	}
	return nil
}

func (f *Fake) GetSandbox(_ context.Context, name string) (*Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return nil, f.Err
	}
	s, ok := f.sandboxes[name]
	if !ok {
		return nil, fmt.Errorf("sandbox %q: %w", name, ErrNotFound)
	}
	cp := *s
	return &cp, nil
}

func (f *Fake) PatchSandboxOperatingMode(_ context.Context, name, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if f.Err != nil {
		return f.Err
	}
	s, ok := f.sandboxes[name]
	if !ok {
		return fmt.Errorf("sandbox %q: %w", name, ErrNotFound)
	}
	s.OperatingMode = mode
	// Mimic the controller settling instantly: mode drives conditions.
	s.Suspended = mode == "Suspended"
	s.Ready = mode == "Running"
	s.ReadyChanged = time.Now()
	return nil
}

func (f *Fake) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Err
}

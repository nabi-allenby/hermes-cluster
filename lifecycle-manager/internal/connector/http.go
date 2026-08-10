package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// throttleWindow mirrors the connector's 60 s auth-failure window with slack.
const throttleWindow = 65 * time.Second

// HTTPClient is the real Client. It never retries: the connector's
// per-source-IP throttle turns retry storms into a 60 s lockout for every
// caller sharing this egress IP, so a failed call waits for the next sweep.
type HTTPClient struct {
	base           string // admin-plane base URL, no trailing slash
	adminToken     string
	provisionToken string
	http           *http.Client

	mu             sync.Mutex
	throttledUntil time.Time
}

// NewHTTPClient builds a client for the connector admin plane. provisionToken
// may be empty; Provision then fails with a config error.
func NewHTTPClient(baseURL, adminToken, provisionToken string) *HTTPClient {
	return &HTTPClient{
		base:           baseURL,
		adminToken:     adminToken,
		provisionToken: provisionToken,
		http:           &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *HTTPClient) Enabled() bool { return true }

// ThrottledUntil exposes the current backoff deadline for /status.
func (c *HTTPClient) ThrottledUntil() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.throttledUntil) {
		return c.throttledUntil, true
	}
	return time.Time{}, false
}

// do performs one request. No retries, ever (see type comment).
func (c *HTTPClient) do(ctx context.Context, method, path, bearer string, body, out interface{}) error {
	c.mu.Lock()
	if time.Now().Before(c.throttledUntil) {
		c.mu.Unlock()
		return ErrThrottled
	}
	c.mu.Unlock()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connector: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		c.mu.Lock()
		c.throttledUntil = time.Now().Add(throttleWindow)
		c.mu.Unlock()
		return ErrThrottled
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Message: readErrorBody(resp.Body)}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("connector: decoding %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func readErrorBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(bytes.TrimSpace(b))
}

func (c *HTTPClient) Provision(ctx context.Context, req ProvisionRequest) (*ProvisionResult, error) {
	if c.provisionToken == "" {
		return nil, fmt.Errorf("connector: no provision token configured (HLM_CONNECTOR_PROVISION_TOKEN)")
	}
	// The response's secret/deliveryKey are deliberately absent from this
	// struct: json.Decode drops unknown fields, so the credential never
	// exists in lifecycle-manager memory beyond the transport buffer.
	var resp struct {
		Tenant    string   `json:"tenant"`
		GatewayID string   `json:"gatewayId"`
		RouteKeys []string `json:"routeKeys"`
	}
	if err := c.do(ctx, http.MethodPost, "/relay/provision", c.provisionToken, req, &resp); err != nil {
		return nil, err
	}
	return &ProvisionResult{Tenant: resp.Tenant, GatewayID: resp.GatewayID, RouteKeys: resp.RouteKeys}, nil
}

func (c *HTTPClient) ListInstances(ctx context.Context) ([]Instance, error) {
	var resp struct {
		Instances []Instance `json:"instances"`
	}
	if err := c.do(ctx, http.MethodGet, "/admin/v1/instances", c.adminToken, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Instances, nil
}

func (c *HTTPClient) PatchInstance(ctx context.Context, gatewayID string, patch InstancePatch) error {
	return c.do(ctx, http.MethodPatch, "/admin/v1/instances/"+url.PathEscape(gatewayID), c.adminToken, patch, nil)
}

func (c *HTTPClient) Revoke(ctx context.Context, gatewayID string) error {
	return c.do(ctx, http.MethodPost, "/admin/v1/instances/"+url.PathEscape(gatewayID)+"/revoke", c.adminToken, nil, nil)
}

func (c *HTTPClient) DeleteInstance(ctx context.Context, gatewayID string) error {
	return c.do(ctx, http.MethodDelete, "/admin/v1/instances/"+url.PathEscape(gatewayID), c.adminToken, nil, nil)
}

func (c *HTTPClient) SetRoute(ctx context.Context, route Route) error {
	return c.do(ctx, http.MethodPost, "/admin/v1/routes", c.adminToken, route, nil)
}

func (c *HTTPClient) ListRoutes(ctx context.Context, gatewayID string) ([]Route, error) {
	path := "/admin/v1/routes"
	if gatewayID != "" {
		path += "?gatewayId=" + url.QueryEscape(gatewayID)
	}
	var resp struct {
		Routes []Route `json:"routes"`
	}
	if err := c.do(ctx, http.MethodGet, path, c.adminToken, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Routes, nil
}

func (c *HTTPClient) DeleteRoute(ctx context.Context, platform, chatID string) error {
	return c.do(ctx, http.MethodDelete,
		"/admin/v1/routes/"+url.PathEscape(platform)+"/"+url.PathEscape(chatID), c.adminToken, nil, nil)
}

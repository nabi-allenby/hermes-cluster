package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"
)

// ErrDisabled is returned by the Disabled client.
var ErrDisabled = errors.New("agent status polling is disabled")

// basicProvider is the upstream password provider's registered name
// (plugins/dashboard_auth/basic — the chart's throwaway credential).
const basicProvider = "basic"

// HTTPClient implements Client against `hermes serve` on session pods.
//
// Mirrors the connector client's shape (no retries, small timeout) with one
// difference: no throttle window — each session pod is its own server, so a
// failure only marks that session unknown, never the whole fleet.
type HTTPClient struct {
	port     int
	username string
	password string
	client   *http.Client

	// One cookie jar for all targets: cookies are host-scoped, so each
	// pod's login session coexists. Re-login happens on 401 (pod restarts
	// mint a fresh signing secret, invalidating old cookies).
	jar http.CookieJar
	mu  sync.Mutex // serializes the login-retry path per client
}

// NewHTTPClient builds a status poller. username/password may be empty —
// then only the public /api/status is usable and CronConfigured is false.
func NewHTTPClient(port int, timeout time.Duration, username, password string) *HTTPClient {
	jar, _ := cookiejar.New(nil) // only errors on bad PublicSuffixList options
	return &HTTPClient{
		port:     port,
		username: username,
		password: password,
		jar:      jar,
		client:   &http.Client{Timeout: timeout, Jar: jar},
	}
}

func (c *HTTPClient) Enabled() bool        { return true }
func (c *HTTPClient) CronConfigured() bool { return c.username != "" && c.password != "" }

func (c *HTTPClient) baseURL(target string) string {
	return fmt.Sprintf("http://%s:%d", target, c.port)
}

// Status fetches the public /api/status readout.
func (c *HTTPClient) Status(ctx context.Context, target string) (*Report, error) {
	if target == "" {
		return nil, errors.New("no status target (sandbox has no serviceFQDN)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL(target)+"/api/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/status: %s", resp.Status)
	}
	var report Report
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&report); err != nil {
		return nil, fmt.Errorf("GET /api/status: %w", err)
	}
	return &report, nil
}

// cronJob is the slice of a cron job record the sweeper reads. Jobs are
// listed with include_disabled=true upstream, so Enabled must be checked.
type cronJob struct {
	Enabled   *bool  `json:"enabled"`
	NextRunAt string `json:"next_run_at"`
}

// NextCronFire returns the earliest upcoming fire among enabled jobs.
func (c *HTTPClient) NextCronFire(ctx context.Context, target string) (*time.Time, error) {
	if !c.CronConfigured() {
		return nil, errors.New("cron credentials not configured")
	}
	if target == "" {
		return nil, errors.New("no status target (sandbox has no serviceFQDN)")
	}
	body, err := c.getWithLogin(ctx, target, "/api/cron/jobs?profile=all")
	if err != nil {
		return nil, err
	}
	var jobs []cronJob
	if err := json.Unmarshal(body, &jobs); err != nil {
		return nil, fmt.Errorf("GET /api/cron/jobs: %w", err)
	}
	var earliest *time.Time
	for _, job := range jobs {
		if job.Enabled != nil && !*job.Enabled {
			continue
		}
		if job.NextRunAt == "" {
			continue
		}
		t, err := parseCronTime(job.NextRunAt)
		if err != nil {
			// One malformed timestamp must not unblock a suspend: the
			// schedule is unknown, and unknown is not "no cron".
			return nil, fmt.Errorf("cron next_run_at %q: %w", job.NextRunAt, err)
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = &t
		}
	}
	return earliest, nil
}

// parseCronTime accepts the isoformat() shapes upstream persists: RFC3339
// with offset, or a naive timestamp (treated as UTC — the scheduler computes
// next_run_at from timezone-aware UTC clocks).
func parseCronTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02T15:04:05", strings.TrimSuffix(s, "Z"))
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// getWithLogin GETs an auth-gated path, minting a password-login session on
// 401 and retrying once. The login mirrors the SPA's credential form:
// POST /auth/password-login {"provider":"basic",...} → Set-Cookie session.
func (c *HTTPClient) getWithLogin(ctx context.Context, target, path string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	body, status, err := c.get(ctx, target, path)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if err := c.login(ctx, target); err != nil {
			return nil, err
		}
		body, status, err = c.get(ctx, target, path)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", path, status)
	}
	return body, nil
}

func (c *HTTPClient) get(ctx context.Context, target, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL(target)+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

func (c *HTTPClient) login(ctx context.Context, target string) error {
	payload, _ := json.Marshal(map[string]string{
		"provider": basicProvider,
		"username": c.username,
		"password": c.password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL(target)+"/auth/password-login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("password login: %s", resp.Status)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

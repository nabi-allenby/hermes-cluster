package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeServe is a contract-faithful stand-in for `hermes serve`: public
// /api/status, cookie-gated /api/cron/jobs, and the password-login route
// minting the session cookie (shapes per NousResearch/hermes-agent@244d296).
type fakeServe struct {
	status  Report
	jobs    []map[string]interface{}
	logins  int
	badPass bool
}

const testCookie = "hermes_session=ok"

func (f *fakeServe) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"version":         "1.0.0",
			"gateway_running": true,
			"active_agents":   f.status.ActiveAgents,
			"active_sessions": f.status.ActiveSessions,
			"gateway_busy":    f.status.ActiveAgents > 0,
		})
	})
	mux.HandleFunc("POST /auth/password-login", func(w http.ResponseWriter, r *http.Request) {
		f.logins++
		var body struct{ Provider, Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.badPass || body.Provider != "basic" || body.Username != "lm" || body.Password != "hunter2" {
			http.Error(w, `{"detail":"Invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "hermes_session", Value: "ok", Path: "/"})
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})
	mux.HandleFunc("GET /api/cron/jobs", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("hermes_session"); err != nil || c.Value != "ok" {
			http.Error(w, `{"detail":"no_cookie"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(f.jobs)
	})
	return mux
}

// start returns a client wired to the fake's host and port.
func start(t *testing.T, f *fakeServe, user, pass string) (*HTTPClient, string) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return NewHTTPClient(port, 2*time.Second, user, pass), u.Hostname()
}

func TestStatusParsesActivityFields(t *testing.T) {
	cli, host := start(t, &fakeServe{status: Report{ActiveAgents: 2, ActiveSessions: 1}}, "", "")
	report, err := cli.Status(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if report.ActiveAgents != 2 || report.ActiveSessions != 1 {
		t.Fatalf("report %+v", report)
	}
	if !report.Busy() {
		t.Fatal("expected Busy")
	}
	if (&Report{}).Busy() {
		t.Fatal("zero report must not be Busy")
	}
}

func TestStatusUnreachableIsAnError(t *testing.T) {
	cli := NewHTTPClient(1, time.Second, "", "")
	if _, err := cli.Status(context.Background(), "127.0.0.1"); err == nil {
		t.Fatal("expected error for a dead port")
	}
	if _, err := cli.Status(context.Background(), ""); err == nil {
		t.Fatal("expected error for an empty target")
	}
}

func TestNextCronFireLogsInAndPicksEarliest(t *testing.T) {
	near := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	far := time.Now().Add(2 * time.Hour).UTC()
	disabledSoon := time.Now().Add(time.Minute).UTC()
	f := &fakeServe{jobs: []map[string]interface{}{
		// isoformat() with offset:
		{"enabled": true, "next_run_at": far.Format(time.RFC3339)},
		// naive isoformat (no offset) — treated as UTC:
		{"enabled": true, "next_run_at": near.Format("2006-01-02T15:04:05")},
		// disabled jobs are listed upstream (include_disabled=true) but must not count:
		{"enabled": false, "next_run_at": disabledSoon.Format(time.RFC3339)},
		// paused/never-scheduled:
		{"enabled": true, "next_run_at": ""},
	}}
	cli, host := start(t, f, "lm", "hunter2")

	got, err := cli.NextCronFire(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Equal(near) {
		t.Fatalf("earliest fire %v, want %v", got, near)
	}
	if f.logins != 1 {
		t.Fatalf("logins = %d, want 1", f.logins)
	}

	// Second call reuses the session cookie — no re-login.
	if _, err := cli.NextCronFire(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 {
		t.Fatalf("logins after cookie reuse = %d, want 1", f.logins)
	}
}

func TestNextCronFireNoJobsIsNil(t *testing.T) {
	cli, host := start(t, &fakeServe{jobs: []map[string]interface{}{}}, "lm", "hunter2")
	got, err := cli.NextCronFire(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("want nil for an empty schedule, got %v", got)
	}
}

func TestNextCronFireBadCredentialsFails(t *testing.T) {
	cli, host := start(t, &fakeServe{badPass: true}, "lm", "wrong")
	if _, err := cli.NextCronFire(context.Background(), host); err == nil {
		t.Fatal("expected error on rejected login")
	}
}

func TestNextCronFireWithoutCredentials(t *testing.T) {
	cli, host := start(t, &fakeServe{}, "", "")
	if cli.CronConfigured() {
		t.Fatal("CronConfigured must be false without credentials")
	}
	if _, err := cli.NextCronFire(context.Background(), host); err == nil {
		t.Fatal("expected error without credentials")
	}
}

func TestNextCronFireMalformedTimestampFailsClosed(t *testing.T) {
	f := &fakeServe{jobs: []map[string]interface{}{
		{"enabled": true, "next_run_at": "yesterday-ish"},
	}}
	cli, host := start(t, f, "lm", "hunter2")
	if _, err := cli.NextCronFire(context.Background(), host); err == nil {
		t.Fatal("a malformed schedule must be an error (unknown ≠ no cron)")
	}
}

func TestParseCronTime(t *testing.T) {
	for _, s := range []string{
		"2026-08-12T10:00:00+00:00",
		"2026-08-12T10:00:00Z",
		"2026-08-12T10:00:00",
	} {
		got, err := parseCronTime(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !strings.HasPrefix(got.UTC().Format(time.RFC3339), "2026-08-12T10:00:00") {
			t.Fatalf("%s parsed to %v", s, got)
		}
	}
}

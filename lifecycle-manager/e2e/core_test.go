//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
)

// TestTier1CoreLifecycle: session CRUD + wake against the real cluster, no
// connector — the generic core standing alone.
func TestTier1CoreLifecycle(t *testing.T) {
	requireCluster(t)
	baseURL, _ := startLM(t, connector.Disabled{}, func(int) string { return "" }, 0)

	id := "e2e-core-1"
	kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found", "--wait=true")

	code, body := httpJSON(t, "POST", baseURL+"/v1/sessions", `{"id":"`+id+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	t.Cleanup(func() { kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found") })

	waitFor(t, "session Ready", 3*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Ready" {
			return "state=" + s
		}
		return ""
	})

	// Suspend out-of-band (as the idle sweeper would), then wake via the API.
	sandbox := kubectl(t, "get", "sandboxclaim", id, "-o", "jsonpath={.status.sandbox.name}")
	kubectl(t, "patch", "sandbox", sandbox, "--type=merge", "-p", `{"spec":{"operatingMode":"Suspended"}}`)
	waitFor(t, "session Suspended", 2*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Suspended" {
			return "state=" + s
		}
		return ""
	})

	code, body = httpJSON(t, "GET", baseURL+"/wake/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("wake: %d %v", code, body)
	}
	waitFor(t, "session Ready after wake", 2*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Ready" {
			return "state=" + s
		}
		return ""
	})

	code, body = httpJSON(t, "DELETE", baseURL+"/v1/sessions/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, body)
	}
	waitFor(t, "session gone", 2*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "GONE" {
			return "state=" + s
		}
		return ""
	})
}

// TestTier1TTLSweep: a short-TTL session is decommissioned by the sweeper,
// cascade included.
func TestTier1TTLSweep(t *testing.T) {
	requireCluster(t)
	baseURL, _ := startLM(t, connector.Disabled{}, func(int) string { return "" }, 3*time.Second)

	id := "e2e-ttl-1"
	kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found", "--wait=true")

	code, body := httpJSON(t, "POST", baseURL+"/v1/sessions", `{"id":"`+id+`","ttlSeconds":10}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	t.Cleanup(func() { kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found") })

	waitFor(t, "TTL sweep removes session", 3*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "GONE" {
			return "state=" + s
		}
		return ""
	})
}

// TestTier1Statelessness: a "restarted" lifecycle-manager (fresh in-process
// instance) sees and manages sessions created by its predecessor.
func TestTier1Statelessness(t *testing.T) {
	requireCluster(t)
	baseURL1, _ := startLM(t, connector.Disabled{}, func(int) string { return "" }, 0)

	id := "e2e-restart-1"
	kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found", "--wait=true")
	code, _ := httpJSON(t, "POST", baseURL1+"/v1/sessions", `{"id":"`+id+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	t.Cleanup(func() { kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found") })

	// Second instance = restart. It must list, read, and delete the session.
	baseURL2, _ := startLM(t, connector.Disabled{}, func(int) string { return "" }, 0)
	waitFor(t, "successor sees session", time.Minute, func() string {
		if s := sessionState(t, baseURL2, id); s == "GONE" {
			return "not visible"
		}
		return ""
	})
	code, body := httpJSON(t, "DELETE", baseURL2+"/v1/sessions/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("delete via successor: %d %v", code, body)
	}
}

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
)

const (
	adminToken = "e2e-admin-token"
	provToken  = "e2e-provision-token"
)

// connectorImage picks the connector image: env override, the published pin,
// or a locally built hrc image.
func connectorImage(t *testing.T) string {
	t.Helper()
	// Tier 2 needs the connector's full admin surface (instance PATCH,
	// routes GET/DELETE, deprovision purge) — v0.2.0 or newer.
	candidates := []string{
		os.Getenv("HLM_E2E_CONNECTOR_IMAGE"),
		"hrc:e2e",
		// The connector's release workflow publishes bare-semver tags
		// (type=semver,pattern={{version}}): 0.2.0, not v0.2.0.
		"ghcr.io/nabi-allenby/hermes-relay-connector:0.2.0",
	}
	for _, img := range candidates {
		if img == "" {
			continue
		}
		if err := exec.Command("docker", "image", "inspect", img).Run(); err == nil {
			return img
		}
		if err := exec.Command("docker", "pull", img).Run(); err == nil {
			return img
		}
	}
	t.Skip("no hermes-relay-connector image available (set HLM_E2E_CONNECTOR_IMAGE)")
	return ""
}

// startConnector runs the connector container with the echo test surface and
// no Discord token. Returns its admin base URL.
func startConnector(t *testing.T) string {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
	img := connectorImage(t)
	name := "hlm-e2e-hrc"
	_ = exec.Command("docker", "rm", "-f", name).Run()

	args := []string{
		"run", "-d", "--name", name,
		"-p", "0:8420",
		// Lets the container reach the host-side lifecycle-manager for the
		// wake poke (native on macOS; host-gateway makes it work on Linux).
		"--add-host", "host.docker.internal:host-gateway",
		"-e", "HRC_ADMIN_TOKEN=" + adminToken,
		"-e", "HRC_PROVISION_TOKEN=" + provToken,
		"-e", "HRC_WAKE_COOLDOWN_SECS=5",
		// HRC_DB stays at the image default /data/hrc.db (the image is
		// FROM scratch — /data is its only writable path).
		img,
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "50", name).CombinedOutput()
			t.Logf("connector logs:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	out, err := exec.Command("docker", "port", name, "8420/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	hostPort := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	base := "http://" + strings.Replace(hostPort, "0.0.0.0", "127.0.0.1", 1)

	waitFor(t, "connector healthz", 30*time.Second, func() string {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return err.Error()
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Sprintf("healthz %d", resp.StatusCode)
		}
		return ""
	})
	return base
}

// echoInbound injects a message as if it arrived from Discord.
func echoInbound(t *testing.T, connectorURL, gatewayID, chatID, text string) map[string]interface{} {
	t.Helper()
	req, _ := http.NewRequest("POST", connectorURL+"/echo/inbound",
		strings.NewReader(fmt.Sprintf(`{"gatewayId":%q,"chatId":%q,"text":%q}`, gatewayID, chatID, text)))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]interface{}
	decodeJSON(t, resp, &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("echo/inbound: %d %v", resp.StatusCode, body)
	}
	return body
}

// TestTier2FullWakeLoop is the agentless wake loop (design §5): provision →
// suspend → Discord message (echo) → durable buffer → wake poke → LM patches
// Running → sandbox resumes. No Discord, no Hermes agent.
func TestTier2FullWakeLoop(t *testing.T) {
	requireCluster(t)
	connectorURL := startConnector(t)

	conn := connector.NewHTTPClient(connectorURL, adminToken, provToken)
	baseURL, _ := startLM(t, conn,
		func(port int) string { return fmt.Sprintf("http://host.docker.internal:%d", port) }, 0)

	id := "e2e-wake-1"
	kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found", "--wait=true")

	code, body := httpJSON(t, "POST", baseURL+"/v1/sessions",
		`{"id":"`+id+`","displayName":"Wake Test","connector":{"routeKeys":[{"platform":"discord","chatId":"chan-42"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	t.Cleanup(func() { kubectl(t, "delete", "sandboxclaim", id, "--ignore-not-found") })

	// Instance registered with our wake URL and route bound.
	instances, err := conn.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	var found *connector.Instance
	for i := range instances {
		if instances[i].GatewayID == id {
			found = &instances[i]
		}
	}
	if found == nil {
		t.Fatalf("instance %s not registered; have %+v", id, instances)
	}
	if found.WakeURL == nil || !strings.Contains(*found.WakeURL, "/wake/"+id) {
		t.Fatalf("wakeUrl not set: %+v", found)
	}
	routes, err := conn.ListRoutes(context.Background(), id)
	if err != nil || len(routes) != 1 || routes[0].ChatID != "chan-42" {
		t.Fatalf("routes: %v err=%v", routes, err)
	}

	waitFor(t, "session Ready", 3*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Ready" {
			return "state=" + s
		}
		return ""
	})

	// Suspend, then send a message. The gateway never connected, so the event
	// buffers durably and the first buffered event fires the wake poke.
	sandbox := kubectl(t, "get", "sandboxclaim", id, "-o", "jsonpath={.status.sandbox.name}")
	kubectl(t, "patch", "sandbox", sandbox, "--type=merge", "-p", `{"spec":{"operatingMode":"Suspended"}}`)
	waitFor(t, "session Suspended", 2*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Suspended" {
			return "state=" + s
		}
		return ""
	})

	result := echoInbound(t, connectorURL, id, "chan-42", "hello, are you awake?")
	if result["buffered"] != true {
		t.Fatalf("expected buffered delivery, got %v", result)
	}

	// The poke is fire-and-forget from the connector; the observable effect
	// is the sandbox flipping back to Running and becoming Ready.
	waitFor(t, "wake poke resumes session", 2*time.Minute, func() string {
		if s := sessionState(t, baseURL, id); s != "Ready" {
			return "state=" + s
		}
		return ""
	})

	// Buffered message still queued for the (nonexistent) gateway.
	instances, _ = conn.ListInstances(context.Background())
	for _, inst := range instances {
		if inst.GatewayID == id && inst.BufferedCount == 0 {
			t.Fatal("buffered message vanished without a gateway to drain it")
		}
	}

	// Decommission: instance + routes purged, claim cascade.
	code, body = httpJSON(t, "DELETE", baseURL+"/v1/sessions/"+id, "")
	if code != http.StatusOK || body["deprovisioned"] != true {
		t.Fatalf("delete: %d %v", code, body)
	}
	instances, _ = conn.ListInstances(context.Background())
	for _, inst := range instances {
		if inst.GatewayID == id {
			t.Fatalf("instance survived deprovision: %+v", inst)
		}
	}
	routes, _ = conn.ListRoutes(context.Background(), id)
	if len(routes) != 0 {
		t.Fatalf("routes survived deprovision: %v", routes)
	}
}

// TestTier2OrphanReport: an out-of-band connector instance shows up in the
// reconcile report and is NOT auto-deleted.
func TestTier2OrphanReport(t *testing.T) {
	requireCluster(t)
	connectorURL := startConnector(t)
	conn := connector.NewHTTPClient(connectorURL, adminToken, provToken)

	if _, err := conn.Provision(context.Background(), connector.ProvisionRequest{
		GatewayID: "manual-gw", Platform: "discord", BotID: "someone-elses-bot",
	}); err != nil {
		t.Fatalf("out-of-band provision: %v", err)
	}

	baseURL, _ := startLM(t, conn,
		func(port int) string { return fmt.Sprintf("http://host.docker.internal:%d", port) }, 2*time.Second)

	waitFor(t, "orphan reported in /status", time.Minute, func() string {
		code, body := httpJSON(t, "GET", baseURL+"/status", "")
		if code != http.StatusOK {
			return fmt.Sprintf("status %d", code)
		}
		rec, _ := body["reconcile"].(map[string]interface{})
		if rec == nil {
			return "no reconcile report yet"
		}
		orphans, _ := rec["instancesWithoutClaims"].([]interface{})
		for _, o := range orphans {
			if o == "manual-gw" {
				return ""
			}
		}
		return fmt.Sprintf("orphans=%v", orphans)
	})

	// Report-only: the instance must still exist.
	instances, err := conn.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if inst.GatewayID == "manual-gw" {
			return
		}
	}
	t.Fatal("manual-gw was deleted — orphan policy must be report-only")
}

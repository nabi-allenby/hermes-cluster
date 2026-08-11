//go:build e2e

// Package e2e drives the lifecycle-manager against a real minikube cluster
// (agent-sandbox installed via test/env/minikube-up.sh) and, for Tier 2, a real
// hermes-relay-connector container. Run with: make e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/httpapi"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/sweeper"
)

const (
	e2eNamespace = "default"
	e2ePool      = "e2e-pool"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("kubectl", append([]string{"-n", e2eNamespace}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func requireCluster(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("kubectl", "get", "crd", "sandboxclaims.extensions.agents.x-k8s.io").CombinedOutput(); err != nil {
		t.Skipf("no cluster with agent-sandbox available (run make minikube-up): %v\n%s", err, out)
	}
	kubectl(t, "apply", "-f", filepath.Join(repoRoot(t), "test", "fixtures", "template.yaml"))
}

// startLM runs the lifecycle-manager in-process on 0.0.0.0:<free port>
// (0.0.0.0 so a connector container can poke /wake via host.docker.internal).
func startLM(t *testing.T, conn connector.Client, wakeBaseURL func(port int) string, sweepInterval time.Duration) (baseURL string, manager *lifecycle.Manager) {
	t.Helper()
	k8sClient, err := k8s.NewDynamicClient(k8s.Options{
		Namespace: e2eNamespace,
		Group:     "agents.x-k8s.io",
		ExtGroup:  "extensions.agents.x-k8s.io",
		Version:   "v1beta1",
	})
	if err != nil {
		t.Fatalf("k8s client: %v", err)
	}

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	manager = &lifecycle.Manager{
		K8s:       k8sClient,
		Connector: conn,
		Defaults: lifecycle.Defaults{
			WarmPool: e2ePool, TTL: 0, IdleTimeout: 30 * time.Minute,
			Platform: "discord", BotID: "e2e-bot",
			WakeBaseURL: wakeBaseURL(port),
		},
		Log: log,
	}
	store := &reconcile.Store{}
	server := &httpapi.Server{Manager: manager, Reconcile: store, Log: log}

	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	if sweepInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		runner := &sweeper.Runner{Manager: manager, Interval: sweepInterval, Reconcile: store, Log: log}
		go runner.Run(ctx)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), manager
}

func httpJSON(t *testing.T, method, url, body string) (int, map[string]interface{}) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]interface{}
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

func decodeJSON(t *testing.T, resp *http.Response, v interface{}) {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
}

// waitFor polls until check returns "" (success) or the timeout elapses.
func waitFor(t *testing.T, what string, timeout time.Duration, check func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if last = check(); last == "" {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timeout waiting for %s: %s", what, last)
}

func sessionState(t *testing.T, baseURL, id string) string {
	code, body := httpJSON(t, "GET", baseURL+"/v1/sessions/"+id, "")
	if code == http.StatusNotFound {
		return "GONE"
	}
	if code != http.StatusOK {
		t.Fatalf("GET session %s: %d %v", id, code, body)
	}
	state, _ := body["state"].(string)
	return state
}

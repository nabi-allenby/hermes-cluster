package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
)

// stubConnector implements connector.Client with programmable behavior.
type stubConnector struct {
	connector.Disabled
	enabled      bool
	provisionErr error
	provisions   []connector.ProvisionRequest
	routes       []connector.Route
	routeErr     error
	deletes      []string
	deleteErr    error
	instances    []connector.Instance
}

func (s *stubConnector) SetRoute(_ context.Context, r connector.Route) error {
	if s.routeErr != nil {
		return s.routeErr
	}
	s.routes = append(s.routes, r)
	return nil
}

func (s *stubConnector) Enabled() bool { return s.enabled }
func (s *stubConnector) Provision(_ context.Context, req connector.ProvisionRequest) (*connector.ProvisionResult, error) {
	if s.provisionErr != nil {
		return nil, s.provisionErr
	}
	s.provisions = append(s.provisions, req)
	return &connector.ProvisionResult{Tenant: "default", GatewayID: req.GatewayID, RouteKeys: req.RouteKeys}, nil
}
func (s *stubConnector) DeleteInstance(_ context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletes = append(s.deletes, id)
	return nil
}
func (s *stubConnector) ListInstances(context.Context) ([]connector.Instance, error) {
	return s.instances, nil
}

func newTestServer(t *testing.T, fake *k8s.Fake, conn connector.Client, apiToken string) *httptest.Server {
	t.Helper()
	manager := &lifecycle.Manager{
		K8s:       fake,
		Connector: conn,
		Defaults: lifecycle.Defaults{
			WarmPool: "pool", TTL: 0, IdleTimeout: 30 * time.Minute,
			Platform: "discord", BotID: "bot", WakeBaseURL: "http://lm:8080",
		},
		Log: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	}
	s := &Server{Manager: manager, APIToken: apiToken, Reconcile: &reconcile.Store{}, Log: manager.Log}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func doReq(t *testing.T, method, url, token, body string) (*http.Response, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader = strings.NewReader(body)
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp, decoded
}

func TestCreateAndGetSession(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	srv := newTestServer(t, fake, connector.Disabled{}, "")

	resp, body := doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-test1"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	if body["id"] != "s-test1" || body["state"] != "Ready" {
		t.Fatalf("unexpected session: %v", body)
	}

	resp, body = doReq(t, "GET", srv.URL+"/v1/sessions/s-test1", "", "")
	if resp.StatusCode != http.StatusOK || body["id"] != "s-test1" {
		t.Fatalf("get: %d %v", resp.StatusCode, body)
	}

	// Duplicate id conflicts.
	resp, _ = doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-test1"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: want 409, got %d", resp.StatusCode)
	}

	// Invalid id.
	resp, _ = doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"NOT_VALID"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid id: want 400, got %d", resp.StatusCode)
	}
}

func TestConnectorBlockRequiresIntegration(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	srv := newTestServer(t, fake, connector.Disabled{}, "")
	resp, _ := doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-c","connector":{"routeKeys":[{"chatId":"123"}]}}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 when connector disabled, got %d", resp.StatusCode)
	}
	if _, err := fake.GetClaim(context.Background(), "s-c"); err == nil {
		t.Fatal("claim should not exist after rejected create")
	}
}

func TestProvisionRollback(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	conn := &stubConnector{enabled: true, provisionErr: &connector.APIError{Status: 500, Message: "boom"}}
	srv := newTestServer(t, fake, conn, "")
	resp, body := doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-r","connector":{"routeKeys":[{"chatId":"123"}]}}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d %v", resp.StatusCode, body)
	}
	if _, err := fake.GetClaim(context.Background(), "s-r"); err == nil {
		t.Fatal("claim not rolled back after provision failure")
	}
}

func TestProvisionWiresWakeURLAndRoutes(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	conn := &stubConnector{enabled: true}
	srv := newTestServer(t, fake, conn, "")
	resp, _ := doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-w","displayName":"Test","connector":{"routeKeys":[{"chatId":"c1"},{"chatId":"c2"}]}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	if len(conn.provisions) != 1 {
		t.Fatalf("want 1 provision, got %d", len(conn.provisions))
	}
	p := conn.provisions[0]
	if p.WakeURL != "http://lm:8080/wake/s-w" {
		t.Fatalf("wakeUrl: %q", p.WakeURL)
	}
	if p.GatewayID != "s-w" || p.BotID != "bot" || p.Platform != "discord" || len(p.RouteKeys) != 2 {
		t.Fatalf("provision request: %+v", p)
	}
	// Chat bindings are explicit admin-API calls, not provision routeKeys.
	if len(conn.routes) != 2 || conn.routes[0].ChatID != "c1" || conn.routes[0].Platform != "discord" || conn.routes[0].GatewayID != "s-w" {
		t.Fatalf("chat routes: %+v", conn.routes)
	}
}

func TestRouteBindFailureRollsBack(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	conn := &stubConnector{enabled: true, routeErr: &connector.APIError{Status: 500, Message: "db"}}
	srv := newTestServer(t, fake, conn, "")
	resp, _ := doReq(t, "POST", srv.URL+"/v1/sessions", "", `{"id":"s-rb","connector":{"routeKeys":[{"chatId":"c1"}]}}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	if _, err := fake.GetClaim(context.Background(), "s-rb"); err == nil {
		t.Fatal("claim not rolled back after route-bind failure")
	}
	if len(conn.deletes) != 1 || conn.deletes[0] != "s-rb" {
		t.Fatalf("instance not deprovisioned on rollback: %v", conn.deletes)
	}
}

func TestWakeIsIdempotentAndUnauthenticated(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-1", time.Now().Add(-time.Hour), nil, "Suspended", false)
	srv := newTestServer(t, fake, connector.Disabled{}, "secret-token")

	// No bearer on /wake even though /v1 is guarded.
	for i := 0; i < 3; i++ {
		resp, body := doReq(t, "GET", srv.URL+"/wake/s-1", "", "")
		if resp.StatusCode != http.StatusOK || body["ok"] != true {
			t.Fatalf("wake #%d: %d %v", i, resp.StatusCode, body)
		}
	}
	sb, err := fake.GetSandbox(context.Background(), "s-1")
	if err != nil || sb.OperatingMode != "Running" {
		t.Fatalf("sandbox not running after wake: %+v err=%v", sb, err)
	}

	resp, _ := doReq(t, "GET", srv.URL+"/wake/unknown", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session wake: want 404, got %d", resp.StatusCode)
	}
}

func TestAPITokenGuardsV1Only(t *testing.T) {
	fake := &k8s.Fake{CreateSandboxOnClaim: true}
	srv := newTestServer(t, fake, connector.Disabled{}, "secret-token")

	resp, _ := doReq(t, "GET", srv.URL+"/v1/sessions", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: want 401, got %d", resp.StatusCode)
	}
	resp, _ = doReq(t, "GET", srv.URL+"/v1/sessions", "wrong", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", resp.StatusCode)
	}
	resp, _ = doReq(t, "GET", srv.URL+"/v1/sessions", "secret-token", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good token: want 200, got %d", resp.StatusCode)
	}
	// Health endpoints stay open.
	resp, _ = doReq(t, "GET", srv.URL+"/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: want 200, got %d", resp.StatusCode)
	}
}

func TestDeleteSessionDeprovisions(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-d", time.Now(), map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
	conn := &stubConnector{enabled: true}
	srv := newTestServer(t, fake, conn, "")

	resp, body := doReq(t, "DELETE", srv.URL+"/v1/sessions/s-d", "", "")
	if resp.StatusCode != http.StatusOK || body["deleted"] != true || body["deprovisioned"] != true {
		t.Fatalf("delete: %d %v", resp.StatusCode, body)
	}
	if len(conn.deletes) != 1 || conn.deletes[0] != "s-d" {
		t.Fatalf("connector deletes: %v", conn.deletes)
	}
}

func TestDeleteKeepsClaimWhenDeprovisionFails(t *testing.T) {
	fake := &k8s.Fake{}
	fake.AddSession("s-k", time.Now(), map[string]string{"hermes.nabi.dev/connector": "true"}, "Running", true)
	conn := &stubConnector{enabled: true, deleteErr: &connector.APIError{Status: 500, Message: "db locked"}}
	srv := newTestServer(t, fake, conn, "")

	resp, _ := doReq(t, "DELETE", srv.URL+"/v1/sessions/s-k", "", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	if _, err := fake.GetClaim(context.Background(), "s-k"); err != nil {
		t.Fatal("claim must be retained for retry after deprovision failure")
	}
	// Retry after connector recovers succeeds and removes the claim.
	conn.deleteErr = nil
	resp, _ = doReq(t, "DELETE", srv.URL+"/v1/sessions/s-k", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry delete: want 200, got %d", resp.StatusCode)
	}
	if _, err := fake.GetClaim(context.Background(), "s-k"); err == nil {
		t.Fatal("claim should be gone after successful retry")
	}
}

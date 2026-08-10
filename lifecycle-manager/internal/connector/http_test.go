package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeConnector mimics the connector's admin plane closely enough to verify
// the client: bearer auth, camelCase JSON, {"error": ...} bodies, and the
// per-source-IP 429 throttle.
type fakeConnector struct {
	t            *testing.T
	adminToken   string
	provToken    string
	throttled    atomic.Bool
	lastReq      atomic.Value // *http.Request URL string
	provisioned  atomic.Value // ProvisionRequest
	patchedBody  atomic.Value // string
	deleteCalled atomic.Int32
}

func (f *fakeConnector) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(token func() string, next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.lastReq.Store(r.URL.String())
			if f.throttled.Load() {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"too many failed auth attempts; retry later"}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+token() {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("POST /relay/provision", auth(func() string { return f.provToken }, func(w http.ResponseWriter, r *http.Request) {
		var req ProvisionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.provisioned.Store(req)
		// Real response carries the gateway secret; the client must drop it.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"secret":      "deadbeef-SECRET-VALUE",
			"deliveryKey": "cafebabe-DELIVERY-KEY",
			"tenant":      "default",
			"gatewayId":   req.GatewayID,
			"routeKeys":   req.RouteKeys,
		})
	}))
	mux.HandleFunc("GET /admin/v1/instances", auth(func() string { return f.adminToken }, func(w http.ResponseWriter, _ *http.Request) {
		in := int64(1754800000)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"instances": []Instance{{
				GatewayID: "s-1", Tenant: "default", Connected: true,
				BufferedCount: 2, LastInboundAt: &in, TurnInFlight: true,
			}},
		})
	}))
	mux.HandleFunc("PATCH /admin/v1/instances/{id}", auth(func() string { return f.adminToken }, func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(json.RawMessage(mustRead(r)))
		f.patchedBody.Store(string(body))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"gatewayId": r.PathValue("id")})
	}))
	mux.HandleFunc("DELETE /admin/v1/instances/{id}", auth(func() string { return f.adminToken }, func(w http.ResponseWriter, r *http.Request) {
		f.deleteCalled.Add(1)
		if r.PathValue("id") == "missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unknown gateway"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"deleted": true, "closedSessions": 1})
	}))
	mux.HandleFunc("POST /admin/v1/routes", auth(func() string { return f.adminToken }, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	mux.HandleFunc("DELETE /admin/v1/routes/{platform}/{chatId}", auth(func() string { return f.adminToken }, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
	}))
	return mux
}

func mustRead(r *http.Request) []byte {
	b := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			return b
		}
	}
}

func newFixture(t *testing.T) (*fakeConnector, *HTTPClient) {
	f := &fakeConnector{t: t, adminToken: "admin-tok", provToken: "prov-tok"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, NewHTTPClient(srv.URL, "admin-tok", "prov-tok")
}

func TestProvisionDiscardsSecret(t *testing.T) {
	f, c := newFixture(t)
	res, err := c.Provision(context.Background(), ProvisionRequest{
		GatewayID: "s-1", Platform: "discord", BotID: "bot", RouteKeys: []string{"chat-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GatewayID != "s-1" || res.Tenant != "default" || len(res.RouteKeys) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got := f.provisioned.Load().(ProvisionRequest)
	if got.WakeURL != "" && !strings.HasPrefix(got.WakeURL, "http") {
		t.Fatalf("wakeUrl mangled: %q", got.WakeURL)
	}
	// The secret must not survive into any reachable field of the result.
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), "SECRET") || strings.Contains(string(blob), "DELIVERY") {
		t.Fatalf("provision result leaked credentials: %s", blob)
	}
}

func TestListInstancesDecodesActivityFields(t *testing.T) {
	_, c := newFixture(t)
	instances, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("want 1 instance, got %d", len(instances))
	}
	inst := instances[0]
	if inst.GatewayID != "s-1" || !inst.Connected || inst.BufferedCount != 2 || !inst.TurnInFlight {
		t.Fatalf("bad decode: %+v", inst)
	}
	if inst.LastInboundAt == nil || *inst.LastInboundAt != 1754800000 {
		t.Fatalf("lastInboundAt: %v", inst.LastInboundAt)
	}
	if inst.LastOutboundAt != nil {
		t.Fatalf("lastOutboundAt should be nil (unknown), got %v", *inst.LastOutboundAt)
	}
}

func TestThrottleShortCircuits(t *testing.T) {
	f, c := newFixture(t)
	f.throttled.Store(true)
	if _, err := c.ListInstances(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("want ErrThrottled, got %v", err)
	}
	// Server recovers, but the client must keep backing off locally without
	// sending another request.
	f.throttled.Store(false)
	f.lastReq.Store("")
	if _, err := c.ListInstances(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("want local ErrThrottled, got %v", err)
	}
	if f.lastReq.Load().(string) != "" {
		t.Fatal("client sent a request while throttled — retry-storm risk")
	}
	if _, ok := c.ThrottledUntil(); !ok {
		t.Fatal("ThrottledUntil should report active backoff")
	}
}

func TestDeleteInstanceNotFound(t *testing.T) {
	_, c := newFixture(t)
	if err := c.DeleteInstance(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := c.DeleteInstance(context.Background(), "s-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestAPIErrorCarriesBody(t *testing.T) {
	f, c := newFixture(t)
	f.adminToken = "rotated" // client now sends a stale token
	_, err := c.ListInstances(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Message != "unauthorized" {
		t.Fatalf("want APIError{401 unauthorized}, got %v", err)
	}
}

func TestRouteSegmentsAreEscaped(t *testing.T) {
	f, c := newFixture(t)
	if err := c.DeleteRoute(context.Background(), "discord", "guild/123:456"); err != nil {
		t.Fatal(err)
	}
	url := f.lastReq.Load().(string)
	if strings.Contains(strings.TrimPrefix(url, "/admin/v1/routes/"), "guild/123") {
		t.Fatalf("chatId not escaped in %q", url)
	}
}

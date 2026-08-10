// Package httpapi is the lifecycle-manager's HTTP surface: generic session
// CRUD under /v1, the unauthenticated /wake/{session} poke target, and
// health/status endpoints. Plain net/http, no framework.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
)

// ThrottleReporter is implemented by the connector HTTP client; the noop
// client doesn't have it, so /status probes via a type assertion.
type ThrottleReporter interface {
	ThrottledUntil() (time.Time, bool)
}

// Server holds handler dependencies.
type Server struct {
	Manager *lifecycle.Manager
	// APIToken, when non-empty, guards /v1/* (never /wake or health: the
	// connector's wake poke is a bare unauthenticated GET).
	APIToken  string
	Reconcile *reconcile.Store
	Log       *slog.Logger
}

// Handler builds the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /status", s.handleStatus)
	// Both methods: the connector pokes with GET; POST is the polite verb
	// for humans and future callers.
	mux.HandleFunc("GET /wake/{session}", s.handleWake)
	mux.HandleFunc("POST /wake/{session}", s.handleWake)

	mux.Handle("POST /v1/sessions", s.auth(s.handleCreateSession))
	mux.Handle("GET /v1/sessions", s.auth(s.handleListSessions))
	mux.Handle("GET /v1/sessions/{id}", s.auth(s.handleGetSession))
	mux.Handle("DELETE /v1/sessions/{id}", s.auth(s.handleDeleteSession))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.APIToken != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.APIToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next(w, r)
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Manager.K8s.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "kubernetes API unreachable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Manager.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byState := map[string]int{}
	for _, sess := range sessions {
		byState[string(sess.State)]++
	}
	connectorStatus := map[string]interface{}{"enabled": s.Manager.Connector.Enabled()}
	if s.Manager.Connector.Enabled() {
		connectorStatus["reachable"] = s.Manager.Connector.Reachable(r.Context())
	}
	if reporter, ok := s.Manager.Connector.(ThrottleReporter); ok {
		if until, throttled := reporter.ThrottledUntil(); throttled {
			connectorStatus["throttledUntil"] = until.Unix()
		}
	}
	status := map[string]interface{}{
		"sessions":  len(sessions),
		"byState":   byState,
		"connector": connectorStatus,
	}
	if s.Reconcile != nil {
		if report, ok := s.Reconcile.Load(); ok {
			status["reconcile"] = report
		}
	}
	writeJSON(w, http.StatusOK, status)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

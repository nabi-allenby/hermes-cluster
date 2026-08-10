package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

// createSessionBody is the POST /v1/sessions request.
type createSessionBody struct {
	ID                 string `json:"id"`
	WarmPool           string `json:"warmPool"`
	TTLSeconds         *int64 `json:"ttlSeconds"`
	IdleTimeoutSeconds *int64 `json:"idleTimeoutSeconds"`
	DisplayName        string `json:"displayName"`
	Connector          *struct {
		RouteKeys []routeKey `json:"routeKeys"`
	} `json:"connector"`
}

type routeKey struct {
	Platform string `json:"platform"`
	ChatID   string `json:"chatId"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req := lifecycle.CreateRequest{
		ID:                 body.ID,
		WarmPool:           body.WarmPool,
		TTLSeconds:         body.TTLSeconds,
		IdleTimeoutSeconds: body.IdleTimeoutSeconds,
		DisplayName:        body.DisplayName,
	}
	if body.Connector != nil {
		req.Connector = true
		for _, rk := range body.Connector.RouteKeys {
			if rk.ChatID == "" {
				writeError(w, http.StatusBadRequest, "connector.routeKeys[].chatId is required")
				return
			}
			req.RouteKeys = append(req.RouteKeys, rk.ChatID)
		}
	}

	sess, err := s.Manager.Create(r.Context(), req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, sess)
	case errors.Is(err, k8s.ErrAlreadyExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, lifecycle.ErrConnectorDisabled):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, connector.ErrThrottled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, session.ErrInvalidID):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Manager.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Manager.Get(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, sess)
	case errors.Is(err, k8s.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown session")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	deprovisioned, err := s.Manager.Decommission(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": true, "deprovisioned": deprovisioned})
	case errors.Is(err, k8s.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown session")
	case errors.Is(err, connector.ErrThrottled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		// Connector deprovision failure: claim retained, redelete retries.
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	err := s.Manager.Wake(r.Context(), r.PathValue("session"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, k8s.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown session")
	default:
		// 500 so the connector's next cooldown-gated poke retries; a lost
		// poke degrades to delivery-on-next-resume, never loss.
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/swaggest/swgui/v5emb"
)

type api struct {
	service *switchService
	apiKey  [sha256.Size]byte
}

type switchSummary struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Actions     []string `json:"actions"`
}

type errorResponse struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	Operation *actionResult `json:"operation,omitempty"`
}

func newHTTPHandler(service *switchService, apiKey string) http.Handler {
	handler := &api{service: service, apiKey: sha256.Sum256([]byte(apiKey))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("GET /openapi.yaml", handler.openAPI)
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusPermanentRedirect)
	})
	mux.Handle("GET /docs/", v5emb.New("Switch API", "/openapi.yaml", "/docs/"))
	mux.Handle("GET /switches", handler.authenticate(http.HandlerFunc(handler.listSwitches)))
	mux.Handle("POST /switches/{id}/on", handler.authenticate(http.HandlerFunc(handler.switchOn)))
	mux.Handle("POST /switches/{id}/off", handler.authenticate(http.HandlerFunc(handler.switchOff)))
	return securityHeaders(mux)
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}

func (a *api) listSwitches(w http.ResponseWriter, _ *http.Request) {
	configured := a.service.list()
	result := make([]switchSummary, 0, len(configured))
	for _, item := range configured {
		result = append(result, switchSummary{
			ID:          item.ID,
			DisplayName: item.DisplayName,
			Actions:     []string{"on", "off"},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"switches": result})
}

func (a *api) switchOn(w http.ResponseWriter, r *http.Request) {
	a.execute(w, r, switchStateOn)
}

func (a *api) switchOff(w http.ResponseWriter, r *http.Request) {
	a.execute(w, r, switchStateOff)
}

func (a *api) execute(w http.ResponseWriter, r *http.Request, state switchState) {
	result, err := a.service.execute(r.Context(), r.PathValue("id"), state)
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if errors.Is(err, errSwitchNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: "switch_not_found", Message: "switch is not configured"})
		return
	}
	status := http.StatusBadGateway
	code := "action_failed"
	message := "a switch action failed"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		status = http.StatusGatewayTimeout
		code = "action_timeout"
		message = "the switch action timed out"
	}
	log.Printf("switch %q %s failed: %v", result.SwitchID, state, err)
	writeJSON(w, status, errorResponse{Code: code, Message: message, Operation: &result})
}

func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("X-Api-Key")))
		if subtle.ConstantTimeCompare(a.apiKey[:], got[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "invalid or missing API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/store"
)

type contextKey string

const principalKey contextKey = "principal"

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func data(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, map[string]any{"data": value})
}

func list(w http.ResponseWriter, value any, page store.Page) {
	writeJSON(w, http.StatusOK, map[string]any{"data": value, "meta": page})
}

func apiError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var body errorBody
	body.Error.Code, body.Error.Message, body.Error.RequestID = code, message, requestID(r)
	writeJSON(w, status, body)
}

func handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if store.IsNotFound(err) {
		apiError(w, r, http.StatusNotFound, "not_found", "요청한 대상을 찾을 수 없습니다")
		return
	}
	apiError(w, r, http.StatusInternalServerError, "internal_error", "요청을 처리하지 못했습니다")
}

func decodeJSON(r *http.Request, target any) error {
	const maxBody = 2 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("JSON 요청을 읽을 수 없습니다: %w", err)
	}
	if len(body) > maxBody {
		return errors.New("요청 본문이 2 MiB 제한을 초과했습니다")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("JSON 요청을 읽을 수 없습니다: %w", err)
	}
	return nil
}

func intPath(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func queryInt(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return value
}

func principal(r *http.Request) auth.Principal {
	p, _ := r.Context().Value(principalKey).(auth.Principal)
	return p
}

func requestID(r *http.Request) string { return r.Header.Get("X-Request-ID") }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func requestAudit(r *http.Request, action, resourceType, resourceID, result, reason string, before, after any) store.AuditEvent {
	p := principal(r)
	var actorID *int64
	if p.User.ID != 0 {
		actorID = &p.User.ID
	}
	return store.AuditEvent{ActorUserID: actorID, ActorUsername: p.User.Username, Action: action, ResourceType: resourceType, ResourceID: resourceID, Before: before, After: after, IPAddress: clientIP(r), UserAgent: r.UserAgent(), Result: result, Reason: reason, RequestID: requestID(r)}
}

func withPrincipal(r *http.Request, p auth.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey, p))
}

// listSort reads the ordering a list request asked for. The key is validated
// against a per-endpoint allowlist in the store layer, so anything unknown
// simply leaves the list in its natural order.
func listSort(r *http.Request) store.Sort {
	return store.Sort{Key: r.URL.Query().Get("sort"), Direction: r.URL.Query().Get("order")}
}

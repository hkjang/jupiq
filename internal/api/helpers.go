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

// queryInt reads an optional non-negative integer query parameter. Empty means
// "not given" and yields fallback; anything that is not a base-10 integer, or
// is negative, is an error so a typo like hub_id=abc is reported instead of
// silently widening the query to "every Hub". Zero and values above an
// endpoint's ceiling pass through unchanged for the store to clamp
// (pageBounds, Metrics 5000, ResourceConsumption 500).
func queryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s은(는) 0 이상의 정수여야 합니다", name)
	}
	return value, nil
}

// queryIntOrReject is queryInt for handlers: a malformed value is answered
// with 400 invalid_query on the spot and ok=false tells the caller to return.
func queryIntOrReject(w http.ResponseWriter, r *http.Request, name string, fallback int) (int, bool) {
	value, err := queryInt(r, name, fallback)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_query", err.Error())
		return 0, false
	}
	return value, true
}

// pageQuery reads page and page_size for list handlers; either being
// malformed answers 400 invalid_query and returns ok=false.
func pageQuery(w http.ResponseWriter, r *http.Request, defaultSize int) (page, size int, ok bool) {
	if page, ok = queryIntOrReject(w, r, "page", 1); !ok {
		return 0, 0, false
	}
	if size, ok = queryIntOrReject(w, r, "page_size", defaultSize); !ok {
		return 0, 0, false
	}
	return page, size, true
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

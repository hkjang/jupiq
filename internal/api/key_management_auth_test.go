package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/auth"
)

func TestAPIKeyCannotManageKeyLifecycle(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	request = withPrincipal(request, auth.Principal{APIKeyID: 42})
	response := httptest.NewRecorder()

	(&Server{}).keysList(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("API key lifecycle request status=%d body=%s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); body == "" || !containsAll(body, "interactive_session_required", "브라우저 로그인 세션") {
		t.Fatalf("unexpected denial body: %s", body)
	}
}

func TestAPIKeyCannotMutateInteractiveAccountSession(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me", strings.NewReader(`{"display_name":"changed"}`))
	request = withPrincipal(request, auth.Principal{APIKeyID: 42})
	response := httptest.NewRecorder()
	called := false

	interactiveSessionOnly(func(http.ResponseWriter, *http.Request) { called = true })(response, request)

	if called || response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "interactive_session_required") {
		t.Fatalf("API key account mutation was not blocked: called=%t status=%d body=%s", called, response.Code, response.Body.String())
	}
}

func TestAPIKeyCannotMutateRBAC(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/roles", nil)
	request = withPrincipal(request, auth.Principal{APIKeyID: 42, UserPermissions: []string{"*"}, APIKeyScopes: []string{"roles:write"}})
	response := httptest.NewRecorder()
	called := false

	interactiveSessionOnly(func(http.ResponseWriter, *http.Request) { called = true })(response, request)

	if called || response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "interactive_session_required") {
		t.Fatalf("API key RBAC mutation was not blocked: called=%t status=%d body=%s", called, response.Code, response.Body.String())
	}
}

func TestRoleDelegationCannotExceedActorPermissions(t *testing.T) {
	if permissionsWithin([]string{"*"}, []string{"roles:write"}) {
		t.Fatal("roles:write alone delegated wildcard permission")
	}
	if !permissionsWithin([]string{"usage:read"}, []string{"roles:write", "usage:*"}) {
		t.Fatal("permission within actor wildcard was rejected")
	}
	if !permissionsWithin(nil, []string{"roles:write"}) {
		t.Fatal("empty role permission set should be safe for ceiling checks")
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

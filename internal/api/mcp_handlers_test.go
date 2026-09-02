package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/auth"
)

func TestMCPToolRequiresToolSpecificScope(t *testing.T) {
	request := httptest.NewRequest("POST", "/mcp", nil)
	p := auth.Principal{UserPermissions: []string{"*"}, APIKeyID: 1, APIKeyScopes: []string{"mcp:use"}}
	request = withPrincipal(request, p)
	_, err := (&Server{}).mcpToolCall(request, []byte(`{"name":"jupiq.dashboard","arguments":{}}`))
	if err == nil || !strings.Contains(err.Error(), "세부 권한") {
		t.Fatalf("expected tool-specific permission error, got %v", err)
	}
}

func TestMCPAcceptsSSE(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Accept", "application/json, text/event-stream")
	if !acceptsSSE(r) {
		t.Fatal("expected SSE content negotiation")
	}
}

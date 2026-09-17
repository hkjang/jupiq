package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/auth"
)

// 저장소 없이 볼 수 있는 부분: 설정 검증과, MCP SSO가 꺼진(설정 문서가 없는)
// 설치에서 401·메타데이터가 전과 똑같은지. 켜진 경로는 통합 테스트가 본다.

func TestValidateSettingsMCPOAuthSection(t *testing.T) {
	valid := map[string]any{"enabled": true, "resource": "https://jupiq.example.test/mcp", "audience": []any{"claude-mcp"}, "scopes": []any{"mcp:use", "dashboard:read"}}
	if err := validateSettingsUpdate(map[string]any{"mcp.oauth": valid}, nil); err != nil {
		t.Fatalf("valid section rejected: %v", err)
	}
	// Off with the seeded defaults must always pass, so the settings form
	// can be saved on an installation that never touched this card.
	if err := validateSettingsUpdate(map[string]any{"mcp.oauth": map[string]any{"enabled": false, "resource": "", "audience": []any{}, "scopes": []any{"mcp:use"}}}, nil); err != nil {
		t.Fatalf("default section rejected: %v", err)
	}
	// A resource is this server's own public address, so a private or even
	// loopback host is legitimate for a developer; only the shape is checked.
	if err := validateSettingsUpdate(map[string]any{"mcp.oauth": map[string]any{"resource": "http://localhost:8080/mcp"}}, nil); err != nil {
		t.Fatalf("loopback resource rejected: %v", err)
	}
	cases := []struct {
		name    string
		section map[string]any
		want    string
	}{
		{"unknown field", map[string]any{"issuer": "x"}, "지원하지 않는"},
		{"enabled not bool", map[string]any{"enabled": "yes"}, "true 또는 false"},
		{"resource with query", map[string]any{"resource": "https://jupiq.example.test/mcp?x=1"}, "쿼리"},
		{"resource with userinfo", map[string]any{"resource": "https://user:pw@jupiq.example.test/mcp"}, "userinfo"},
		{"resource without scheme", map[string]any{"resource": "jupiq.example.test/mcp"}, "http"},
		{"resource with whitespace", map[string]any{"resource": "https://jupiq.example.test/m cp"}, "mcp.oauth.resource"},
		{"audience not array", map[string]any{"audience": "claude-mcp"}, "audience"},
		{"audience with space", map[string]any{"audience": []any{"claude mcp"}}, "audience"},
		{"scopes not array", map[string]any{"scopes": "mcp:use"}, "scopes"},
		{"enabled without scopes", map[string]any{"enabled": true}, "scopes"},
		{"enabled with empty scopes", map[string]any{"enabled": true, "scopes": []any{}}, "scopes"},
		{"scope with space", map[string]any{"scopes": []any{"mcp use"}}, "scopes"},
	}
	for _, tc := range cases {
		err := validateSettingsUpdate(map[string]any{"mcp.oauth": tc.section}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v want %q", tc.name, err, tc.want)
		}
	}
}

func TestMCPOAuthOffLeavesRefusalsUnchanged(t *testing.T) {
	logs := &bytes.Buffer{}
	s := &Server{Auth: &auth.Service{}, Logger: slog.New(slog.NewTextHandler(logs, nil))}
	handler := s.Handler()

	// The metadata document does not exist while the feature is off: a
	// client that found it would loop between this server and Keycloak.
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "mcp_oauth_disabled") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS opened on a refusal", path)
		}
	}

	// No bearer at all: 401 as before, and no pointer to an authorization
	// server that is not configured.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("no bearer: %d WWW-Authenticate=%q body=%s", rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "인증이 필요합니다") {
		t.Errorf("refusal wording changed: %s", rec.Body.String())
	}
	if logs.Len() != 0 && strings.Contains(logs.String(), "mcp oauth") {
		t.Errorf("an installation without MCP OAuth logged about it: %s", logs.String())
	}
}

func TestMCPUnauthorizedLogsTheOriginalCauseAndAnswersWithTheGuidance(t *testing.T) {
	logs := &bytes.Buffer{}
	s := &Server{Auth: &auth.Service{}, Logger: slog.New(slog.NewTextHandler(logs, nil))}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer a.b.c")
	rec := httptest.NewRecorder()
	s.mcpUnauthorized(rec, r, &auth.MCPOAuthRefusal{Message: "클라이언트용 안내", Cause: io.ErrUnexpectedEOF})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "클라이언트용 안내") {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), io.ErrUnexpectedEOF.Error()) {
		t.Error("the internal cause leaked to the client")
	}
	if !strings.Contains(logs.String(), "mcp oauth token refused") || !strings.Contains(logs.String(), io.ErrUnexpectedEOF.Error()) {
		t.Errorf("the original verification error was not logged: %s", logs.String())
	}
}

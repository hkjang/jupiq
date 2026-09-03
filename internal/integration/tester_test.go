package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoJSONAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hub/api/info" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-value" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "5.3.0"})
	}))
	defer server.Close()
	var payload map[string]any
	status, _, err := doJSON(context.Background(), server.Client(), http.MethodGet, server.URL+"/hub/api/info", "token-value", nil, &payload, false)
	if err != nil || status != http.StatusOK || payload["version"] != "5.3.0" {
		t.Fatalf("unexpected response: status=%d payload=%#v err=%v", status, payload, err)
	}
}

func TestDoJSONJupyterHubTokenAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token hub-token-value" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "5.3.0"})
	}))
	defer server.Close()
	var payload map[string]any
	status, _, err := doJSONWithAuthScheme(context.Background(), server.Client(), http.MethodGet, server.URL, "hub-token-value", "token", nil, &payload, false)
	if err != nil || status != http.StatusOK || payload["version"] != "5.3.0" {
		t.Fatalf("unexpected response: status=%d payload=%#v err=%v", status, payload, err)
	}
}

func TestConnectionAliasesOnValidationFailure(t *testing.T) {
	result := TestConnection(context.Background(), TestRequest{Type: "jupyterhub", Config: map[string]any{"base_url": "http://127.0.0.1:1"}})
	if result.Success || result.ResponseTimeMS != result.LatencyMS || len(result.Steps) == 0 || result.Guidance == "" {
		t.Fatalf("aliases not finalized: %#v", result)
	}
}

func TestOIDCDiscoveryNeverUsesClientSecretAsBearerToken(t *testing.T) {
	if integrationTestAuthSecret("oidc", "must-not-be-sent") != "" {
		t.Fatal("OIDC discovery retained a client secret for Authorization")
	}
	if integrationTestAuthSecret("prometheus", "token") != "token" {
		t.Fatal("authenticated provider test unexpectedly lost its token")
	}
}

func TestLLMConnectionRejectsSensitiveLabelMappingBeforeNetwork(t *testing.T) {
	result := TestConnection(context.Background(), TestRequest{Type: "llm_usage", Config: map[string]any{
		"base_url":           "https://example.com",
		"source":             "prometheus",
		"pod_username_regex": `^jupyter-(?P<username>[a-z0-9-]+)$`,
		"label_mappings":     map[string]any{"model": "authorization"},
	}})
	if result.Success || !strings.Contains(result.Error, "허용되지 않은") {
		t.Fatalf("sensitive mapping was not rejected: %#v", result)
	}
}

func TestInspectLLMPrometheusResultValidatesCanonicalLabels(t *testing.T) {
	response := map[string]any{"data": map[string]any{"result": []any{
		map[string]any{"metric": map[string]any{"kubernetes_pod_name": "jupyter-user01", "http_route": "/v1/chat/completions", "status": "200", "model": "llama"}},
	}}}
	count, fields, err := inspectLLMPrometheusResult(response, map[string]string{"pod": "kubernetes_pod_name", "path": "http_route"})
	if err != nil || count != 1 || strings.Join(fields, ",") != "pod,path,status,model" {
		t.Fatalf("valid LLM vector rejected: count=%d fields=%v err=%v", count, fields, err)
	}
	missingPath := map[string]any{"data": map[string]any{"result": []any{map[string]any{"metric": map[string]any{"pod": "jupyter-user01"}}}}}
	if _, _, err := inspectLLMPrometheusResult(missingPath, nil); err == nil {
		t.Fatal("sample without canonical path was accepted")
	}
	empty := map[string]any{"data": map[string]any{"result": []any{}}}
	if count, _, err := inspectLLMPrometheusResult(empty, nil); err != nil || count != 0 {
		t.Fatalf("empty successful vector misclassified: count=%d err=%v", count, err)
	}
}

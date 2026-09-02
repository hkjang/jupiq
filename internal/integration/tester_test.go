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

func TestConnectionAliasesOnValidationFailure(t *testing.T) {
	result := TestConnection(context.Background(), TestRequest{Type: "jupyterhub", Config: map[string]any{"base_url": "http://127.0.0.1:1"}})
	if result.Success || result.ResponseTimeMS != result.LatencyMS || len(result.Steps) == 0 || result.Guidance == "" {
		t.Fatalf("aliases not finalized: %#v", result)
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

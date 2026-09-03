package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateLLMUsageRejectsSensitiveLabelMapping(t *testing.T) {
	err := validateLLMUsage(map[string]any{
		"pod_username_regex": `^jupyter-(?P<username>[a-z0-9-]+)$`,
		"label_mappings":     map[string]any{"model": "authorization"},
	})
	if err == nil || !strings.Contains(err.Error(), "허용되지 않은") {
		t.Fatalf("sensitive mapping was not rejected: %v", err)
	}
}

func TestValidateSettingsRejectsFractionalIntegerFields(t *testing.T) {
	err := validateSettingsUpdate(map[string]any{"ai": map[string]any{"max_tokens": 1.5}}, nil)
	if err == nil {
		t.Fatal("fractional max_tokens was accepted")
	}
}

func TestValidateSettingsAllowsOnlyEffectiveSystemFields(t *testing.T) {
	if err := validateSettingsUpdate(map[string]any{"system": map[string]any{"raw_retention_days": 30.0}}, nil); err != nil {
		t.Fatalf("effective retention setting was rejected: %v", err)
	}
	for _, field := range []string{"service_name", "locale", "timezone", "collection_interval_seconds", "page_size"} {
		err := validateSettingsUpdate(map[string]any{"system": map[string]any{field: "unused"}}, nil)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("inactive system field %q was accepted: %v", field, err)
		}
	}
	if err := validateSettingsUpdate(map[string]any{"jupyterhub": map[string]any{"base_url": "https://hub.internal"}}, nil); err == nil {
		t.Fatal("obsolete generic JupyterHub setting was accepted")
	}
	if err := validateSettingsUpdate(map[string]any{"prometheus": map[string]any{"timeout_seconds": 30.0}}, nil); err == nil {
		t.Fatal("inactive Prometheus timeout setting was accepted")
	}
}

func TestValidateSettingsRejectsMalformedOrIncompleteAPIKeyPolicy(t *testing.T) {
	valid := map[string]any{"security": map[string]any{"key_rotation_days": 90.0, "key_max_lifetime_days": 365.0, "key_permissions": []any{"usage:read"}}}
	if err := validateSettingsUpdate(valid, nil); err != nil {
		t.Fatalf("valid API key policy was rejected: %v", err)
	}
	invalid := []map[string]any{
		{"key_rotation_days": 90.0, "key_max_lifetime_days": 365.0, "key_permissions": "usage:read"},
		{"key_rotation_days": 366.0, "key_max_lifetime_days": 365.0, "key_permissions": []any{}},
		{"key_rotation_days": 90.0, "key_max_lifetime_days": 365.0},
	}
	for _, policy := range invalid {
		if err := validateSettingsUpdate(map[string]any{"security": policy}, nil); err == nil {
			t.Fatalf("invalid API key policy was accepted: %#v", policy)
		}
	}
}

func TestValidateSettingsRejectsMalformedStructuredFields(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
	}{
		{
			name: "prometheus queries must be an object",
			values: map[string]any{"prometheus": map[string]any{
				"enabled": true, "base_url": "https://metrics.internal", "verify_tls": true, "queries": "oops",
			}},
		},
		{
			name: "oidc scopes must be an array",
			values: map[string]any{"auth.oidc": map[string]any{
				"enabled": true, "issuer_url": "https://sso.internal/realms/jupiq", "client_id": "jupiq",
				"username_claim": "preferred_username", "scopes": "openid", "verify_tls": true,
			}},
		},
		{
			name: "kubernetes namespace must be a string",
			values: map[string]any{"kubernetes": map[string]any{
				"enabled": true, "base_url": "https://kubernetes.internal", "namespace": 42, "verify_tls": true,
			}},
		},
		{
			name: "llm promql must be an object",
			values: map[string]any{"llm_usage": map[string]any{
				"source": "prometheus", "pod_username_regex": `^jupyter-(?P<username>.+)$`,
				"path_matcher": "/v1/chat/completions", "promql": "sum(http_requests_total)",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSettingsUpdate(test.values, nil); err == nil {
				t.Fatalf("malformed structured setting was accepted: %#v", test.values)
			}
		})
	}
}

func TestValidateSettingsRejectsIncompleteEnabledOIDC(t *testing.T) {
	base := map[string]any{
		"enabled": true, "issuer_url": "https://sso.internal/realms/jupiq", "client_id": "jupiq",
		"username_claim": "preferred_username", "scopes": []any{"openid", "profile"}, "verify_tls": true,
	}
	for _, missing := range []string{"username_claim", "scopes"} {
		candidate := make(map[string]any, len(base))
		for key, value := range base {
			candidate[key] = value
		}
		delete(candidate, missing)
		if err := validateSettingsUpdate(map[string]any{"auth.oidc": candidate}, nil); err == nil {
			t.Fatalf("enabled OIDC without %s was accepted", missing)
		}
	}
}

func TestValidateSettingsRejectsInvalidKubernetesPodPattern(t *testing.T) {
	err := validateSettingsUpdate(map[string]any{"kubernetes": map[string]any{
		"enabled": false, "base_url": "", "verify_tls": true,
		"pod_username_regex": `^jupyter-(.+)$`,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("invalid Kubernetes Pod username pattern was accepted: %v", err)
	}
}

func TestSettingsPutRejectsUnapprovedSecretFieldsBeforePersistence(t *testing.T) {
	for _, body := range []string{
		`{"system":{"raw_retention_days":30,"password":"must-not-store"}}`,
		`{"prometheus":{"enabled":false,"base_url":"","verify_tls":true,"password":"must-not-store"}}`,
		`{"rogue.api_key":"must-not-store"}`,
		`{"ai":{"api_key":42}}`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
		response := httptest.NewRecorder()
		(&Server{}).settingsPut(response, req)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"unsupported_secret"`) {
			t.Fatalf("unapproved secret was not rejected before store access: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestFeatureStatusReturnsOnlyNavigationFlags(t *testing.T) {
	settings := map[string]json.RawMessage{
		"features":  json.RawMessage(`{"gpu_monitoring":true,"llm_usage_monitoring":true,"internal_flag":true}`),
		"workflow":  json.RawMessage(`{"approval_enabled":true,"require_reason":true}`),
		"auth.oidc": json.RawMessage(`{"client_id":"must-not-leak"}`),
	}

	got := featureStatus(settings)
	if !got["gpu_monitoring"] || !got["llm_usage_monitoring"] || !got["approval_workflow"] {
		t.Fatalf("unexpected feature status: %#v", got)
	}
	if len(got) != 3 {
		t.Fatalf("feature endpoint exposed extra settings: %#v", got)
	}
}

func TestDashboardCountsSameUsernamePerHubIdentity(t *testing.T) {
	snapshot := map[string]any{
		"feature_enabled": map[string]bool{"gpu_monitoring": false},
		"live_users": []map[string]any{
			{"hub_id": int64(1), "username": "same-user", "stale": false},
			{"hub_id": int64(2), "username": "same-user", "stale": false},
		},
	}
	applyDashboardFilters(snapshot, httptest.NewRequest("GET", "/api/v1/dashboard", nil))
	summary := snapshot["summary"].(map[string]any)
	if summary["active_users"] != 2 {
		t.Fatalf("cross-Hub identities were collapsed: %#v", summary)
	}
	if _, exists := summary["gpu_usage"]; exists {
		t.Fatalf("missing GPU samples were reported as zero usage: %#v", summary)
	}
}

func TestFeatureStatusDefaultsSafelyForInvalidSettings(t *testing.T) {
	got := featureStatus(map[string]json.RawMessage{
		"features": json.RawMessage(`not-json`),
		"workflow": json.RawMessage(`{"approval_enabled":"invalid"}`),
	})
	for key, enabled := range got {
		if enabled {
			t.Fatalf("%s unexpectedly enabled: %#v", key, got)
		}
	}
}

func TestRedactDisabledMonitoringRemovesEveryCachedFeatureField(t *testing.T) {
	snapshot := map[string]any{
		"feature_enabled": map[string]any{"gpu_monitoring": true, "llm_usage_monitoring": true},
		"gpu_users":       float64(2),
		"gpu_usage":       []any{map[string]any{"username": "sensitive"}},
		"gpu_waste":       []any{map[string]any{"username": "sensitive"}},
		"summary": map[string]any{
			"active_users": float64(1), "gpu_usage": float64(2), "vram_usage": float64(1024),
			"gpu_utilization": float64(90), "vram_utilization": float64(75),
		},
		"live_users": []any{map[string]any{
			"username": "user01", "cpu_cores": float64(1), "gpu_count": float64(2),
			"gpu_utilization": float64(90), "vram_bytes": float64(1024),
			"gpu_sampled_at": "2026-09-03T00:00:00Z", "waste_candidate": true, "waste_score": float64(10),
		}},
		"sessions": []any{map[string]any{"username": "user01", "gpu_count": float64(2)}},
		"aggregates": map[string]any{
			"all": map[string]any{"servers": float64(1), "gpu_count": float64(2), "vram_bytes": float64(1024), "gpu_samples": float64(1)},
		},
		"llm_usage": map[string]any{"feature_enabled": true, "data": []any{map[string]any{"username": "user01", "calls": float64(99)}}},
	}

	redactDisabledMonitoring(snapshot, map[string]bool{"gpu_monitoring": false, "llm_usage_monitoring": false})
	flags := snapshot["feature_enabled"].(map[string]bool)
	if flags["gpu_monitoring"] || flags["llm_usage_monitoring"] {
		t.Fatalf("disabled flags were not authoritative: %#v", flags)
	}
	for _, key := range []string{"gpu_users", "gpu_usage", "gpu_waste", "waste_users"} {
		if _, exists := snapshot[key]; exists {
			t.Fatalf("top-level GPU key %q survived: %#v", key, snapshot[key])
		}
	}
	assertNoGPU := func(name string, record map[string]any) {
		t.Helper()
		for _, key := range []string{"gpu_count", "gpu_usage", "gpu_utilization", "vram_bytes", "vram_usage", "gpu_samples", "gpu_sampled_at", "waste_candidate", "waste_score"} {
			if _, exists := record[key]; exists {
				t.Fatalf("%s retained %q: %#v", name, key, record)
			}
		}
	}
	assertNoGPU("summary", snapshot["summary"].(map[string]any))
	assertNoGPU("live user", snapshot["live_users"].([]any)[0].(map[string]any))
	assertNoGPU("session", snapshot["sessions"].([]any)[0].(map[string]any))
	assertNoGPU("aggregate", snapshot["aggregates"].(map[string]any)["all"].(map[string]any))
	llm := snapshot["llm_usage"].(map[string]any)
	if llm["feature_enabled"] != false || len(llm["data"].([]any)) != 0 || len(llm["top_callers"].([]any)) != 0 {
		t.Fatalf("LLM cached payload survived OFF redaction: %#v", llm)
	}
}

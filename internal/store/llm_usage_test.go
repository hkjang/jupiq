package store

import "testing"

func TestLLMUsageSummaryAndBreakdown(t *testing.T) {
	p95a, p95b := 120.0, 240.0
	inputA, outputA, totalA := int64(10), int64(5), int64(15)
	costA, costB := .25, .10
	items := []map[string]any{{"username": "user01", "pod_name": "jupyter-user01-a", "model": "m1", "calls": int64(8), "success": int64(7), "errors": int64(1), "latency_p95_ms": &p95a, "input_tokens": &inputA, "output_tokens": &outputA, "total_tokens": &totalA, "estimated_cost": &costA}, {"username": "user01", "pod_name": "jupyter-user01-b", "model": "m2", "calls": int64(2), "success": int64(2), "errors": int64(0), "latency_p95_ms": &p95b, "estimated_cost": &costB}}
	summary := summarizeLLMItems(items)
	if summary["calls"] != int64(10) || summary["success_rate"].(float64) != .9 || summary["latency_p95_ms"] != 240.0 || summary["estimated_cost"].(float64) != .35 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	breakdown := buildLLMBreakdown(items)
	if len(breakdown) != 1 || len(breakdown[0]["pods"].([]map[string]any)) != 2 {
		t.Fatalf("unexpected breakdown: %#v", breakdown)
	}
}

func TestLLMSafeLabelsExcludePromptAndCredentials(t *testing.T) {
	labels := llmSafeLabels(map[string]string{
		"pod": "jupyter-user01", "path": "/v1/chat/completions", "status": "200", "model": "model-a",
		"prompt": "민감한 질문", "response": "민감한 응답", "authorization": "Bearer secret", "api_key": "secret",
	})
	if labels["pod"] != "jupyter-user01" || labels["model"] != "model-a" {
		t.Fatalf("required labels missing: %#v", labels)
	}
	for _, key := range []string{"prompt", "response", "authorization", "api_key"} {
		if _, exists := labels[key]; exists {
			t.Fatalf("sensitive label %q was retained", key)
		}
	}
}

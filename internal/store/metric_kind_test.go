package store

import "testing"

func TestIsGPUMetricChecksConfiguredAliasAndQuery(t *testing.T) {
	for name, query := range map[string]string{
		"gpu_utilization":         "up",
		"accelerator_utilization": "avg(DCGM_FI_DEV_GPU_UTIL) by (pod)",
		"model_vram":              "custom_metric",
	} {
		if !IsGPUMetric(name, query) {
			t.Errorf("GPU metric alias was not classified: name=%q query=%q", name, query)
		}
	}
	if IsGPUMetric("cpu_cores", `rate(container_cpu_usage_seconds_total[5m])`) {
		t.Fatal("CPU query was classified as GPU")
	}
}

func TestRemoteMetricIdentityLabelsAreRemovedUntilInventoryResolution(t *testing.T) {
	original := map[string]string{
		"pod": "unknown-pod", "username": "victim", "user": "victim",
		"hub": "spoofed", "network": "spoofed", "department": "spoofed", "project": "spoofed",
		"path": "/v1/chat/completions", "status": "200",
	}
	labels := cloneMetricLabels(original)
	clearMetricIdentityLabels(labels)
	for _, key := range []string{"username", "user", "hub", "network", "department", "project"} {
		if labels[key] != "" {
			t.Fatalf("untrusted identity label %q survived: %#v", key, labels)
		}
	}
	if labels["pod"] != "unknown-pod" || labels["path"] != "/v1/chat/completions" || original["username"] != "victim" {
		t.Fatalf("operational labels or caller input were mutated: labels=%#v original=%#v", labels, original)
	}
}

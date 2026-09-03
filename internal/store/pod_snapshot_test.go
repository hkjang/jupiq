package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
)

func TestSafePodSnapshotExcludesLabelsAndRawMetadata(t *testing.T) {
	raw := safePodSnapshot(integration.Pod{
		Name: "jupyter-user01", Namespace: "jupyter", Node: "worker-1", Phase: "Running",
		Labels: map[string]string{"username": "user01", "authorization": "Bearer must-not-leak"},
	})
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 4 || snapshot["name"] != "jupyter-user01" || snapshot["phase"] != "Running" {
		t.Fatalf("unexpected safe snapshot: %#v", snapshot)
	}
	if strings.Contains(string(raw), "authorization") || strings.Contains(string(raw), "must-not-leak") {
		t.Fatalf("sensitive label leaked into snapshot: %s", raw)
	}
}

func TestGPUMetricSnapshotDoesNotInventOrExposeStaleValues(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	utilization, vram, sampled, stale := gpuMetricSnapshot(map[string]any{}, now, 60)
	if utilization != nil || vram != nil || sampled != nil || !stale {
		t.Fatalf("missing metrics were presented as values: util=%v vram=%v sampled=%v stale=%t", utilization, vram, sampled, stale)
	}
	resourceOnly := now.Add(-time.Minute).Format(time.RFC3339)
	utilization, vram, sampled, stale = gpuMetricSnapshot(map[string]any{"gpu_utilization": 83.0, "resource_sampled_at": resourceOnly}, now, 60)
	if utilization != nil || vram != nil || sampled != nil || !stale {
		t.Fatalf("non-GPU resource timestamp marked GPU data fresh: util=%v vram=%v sampled=%v stale=%t", utilization, vram, sampled, stale)
	}
	old := now.Add(-10 * time.Minute).Format(time.RFC3339)
	utilization, vram, _, stale = gpuMetricSnapshot(map[string]any{"gpu_utilization": 83.0, "vram_bytes": 1024.0, "gpu_sampled_at": old}, now, 60)
	if utilization != nil || vram != nil || !stale {
		t.Fatalf("stale metrics remained visible: util=%v vram=%v stale=%t", utilization, vram, stale)
	}
	freshResource := now.Add(-time.Minute).Format(time.RFC3339)
	utilization, vram, _, stale = gpuMetricSnapshot(map[string]any{"gpu_utilization": 83.0, "vram_bytes": 1024.0, "gpu_sampled_at": old, "resource_sampled_at": freshResource}, now, 60)
	if utilization != nil || vram != nil || !stale {
		t.Fatalf("fresh CPU/resource timestamp masked stale GPU metrics: util=%v vram=%v stale=%t", utilization, vram, stale)
	}
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	utilization, vram, _, stale = gpuMetricSnapshot(map[string]any{"gpu_utilization": 83.0, "gpu_sampled_at": fresh}, now, 60)
	if stale || utilization != 83.0 || vram != nil {
		t.Fatalf("fresh partial metrics were misrepresented: util=%v vram=%v stale=%t", utilization, vram, stale)
	}
}

func TestGPUWasteAssessmentRequiresFreshAllocatedLowUtilization(t *testing.T) {
	candidate, score := gpuWasteAssessment(1, 3, 31*60, true)
	if !candidate || score != 97 {
		t.Fatalf("expected GPU waste candidate: candidate=%t score=%v", candidate, score)
	}
	for _, test := range []struct {
		count, util, runtime float64
		fresh                bool
	}{{0, 0, 3600, true}, {1, 5, 3600, true}, {1, 0, 29 * 60, true}, {1, 0, 3600, false}} {
		if candidate, _ := gpuWasteAssessment(test.count, test.util, test.runtime, test.fresh); candidate {
			t.Fatalf("false GPU waste candidate for %#v", test)
		}
	}
}

func TestMetricSafeLabelsExcludeUntrustedRemoteContent(t *testing.T) {
	labels := metricSafeLabels(map[string]string{
		"pod": "jupyter-user01", "namespace": "jupyter", "status": "200",
		"prompt": "must-not-leak", "authorization": "Bearer secret", "response": "private answer",
	})
	if labels["pod"] != "jupyter-user01" || labels["namespace"] != "jupyter" {
		t.Fatalf("operational labels missing: %#v", labels)
	}
	for _, key := range []string{"prompt", "authorization", "response"} {
		if _, exists := labels[key]; exists {
			t.Fatalf("sensitive metric label %q survived: %#v", key, labels)
		}
	}
}

package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
)

func float64Pointer(value float64) *float64 { return &value }

func TestMergeLLMMetricSamplesAddsRawCountersAndUsesMaximumLatency(t *testing.T) {
	earlier := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	later := earlier.Add(5 * time.Second)
	merged, err := mergeLLMMetricSamples(
		llmMetricSample{counterValue: float64Pointer(.4), estimatedCost: float64Pointer(.25), latencyP95: float64Pointer(120), sampledAt: later},
		llmMetricSample{counterValue: float64Pointer(.4), estimatedCost: float64Pointer(.50), latencyP95: float64Pointer(80), sampledAt: earlier},
	)
	if err != nil {
		t.Fatal(err)
	}
	merged, err = mergeLLMMetricSamples(merged, llmMetricSample{counterValue: float64Pointer(.4)})
	if err != nil {
		t.Fatal(err)
	}
	if merged.counterValue == nil || math.Abs(*merged.counterValue-1.2) > 1e-12 || merged.estimatedCost == nil || *merged.estimatedCost != .75 {
		t.Fatalf("additive LLM metric merge failed: %#v", merged)
	}
	calls, success, failures, _, _, _, _, err := finalizeLLMCounter("calls", merged.counterValue, "200")
	if err != nil || calls != 1 || success != 1 || failures != 0 {
		t.Fatalf("canonical sum must be rounded exactly once: calls=%d success=%d failures=%d err=%v", calls, success, failures, err)
	}
	if merged.latencyP95 == nil || *merged.latencyP95 != 120 {
		t.Fatalf("latency gauge must retain conservative maximum: %#v", merged.latencyP95)
	}
	if !merged.sampledAt.Equal(later) {
		t.Fatalf("latest sample timestamp was not retained: %s", merged.sampledAt)
	}
}

func TestMergeLLMMetricSamplesRejectsOverflow(t *testing.T) {
	_, err := mergeLLMMetricSamples(llmMetricSample{counterValue: float64Pointer(float64(math.MaxInt64) / 2)}, llmMetricSample{counterValue: float64Pointer(float64(math.MaxInt64) / 2)})
	if err == nil {
		t.Fatal("bigint overflow was accepted")
	}
}

func TestFinalizeLLMCounterAssignsMetricColumn(t *testing.T) {
	for _, metric := range []string{"input_tokens", "output_tokens", "total_tokens", "bytes"} {
		calls, success, failures, input, output, total, byteCount, err := finalizeLLMCounter(metric, float64Pointer(2.6), "")
		if err != nil || calls != 0 || success != 0 || failures != 0 {
			t.Fatalf("%s finalize failed: calls=%d err=%v", metric, calls, err)
		}
		values := map[string]*int64{"input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": byteCount}
		if values[metric] == nil || *values[metric] != 3 {
			t.Fatalf("%s was not assigned after rounding: %#v", metric, values)
		}
	}
}

func TestSelectPodCandidatePrefersRunningAndFailsClosedOnTie(t *testing.T) {
	failed := podReconciliationCandidate{pod: integration.Pod{Name: "failed", Phase: "Failed"}, priority: podPhasePriority("Failed")}
	pending := podReconciliationCandidate{pod: integration.Pod{Name: "pending", Phase: "Pending"}, priority: podPhasePriority("Pending")}
	running := podReconciliationCandidate{pod: integration.Pod{Name: "running", Phase: "Running"}, priority: podPhasePriority("Running")}
	selected, ok := selectPodCandidate([]podReconciliationCandidate{running, failed, pending})
	if !ok || selected.pod.Name != "running" {
		t.Fatalf("Running Pod was not preferred: %#v %v", selected, ok)
	}
	otherRunning := podReconciliationCandidate{pod: integration.Pod{Name: "running-b", Phase: "Running"}, priority: podPhasePriority("Running")}
	if _, ok := selectPodCandidate([]podReconciliationCandidate{otherRunning, running}); ok {
		t.Fatal("same-priority multiple Pod candidates must fail closed")
	}
}

func TestTrustedPodUsernameRejectsConflictingLabels(t *testing.T) {
	if username, ok := trustedPodUsername(map[string]string{"hub.jupyter.org/username": "user-a", "jupyterhub-user": "user-b"}); ok || username != "" {
		t.Fatalf("conflicting trusted labels were accepted: username=%q ok=%t", username, ok)
	}
	if username, ok := trustedPodUsername(map[string]string{"username": "spoofed", "hub.jupyter.org/username": "user-a"}); !ok || username != "user-a" {
		t.Fatalf("generic username label overrode trusted label: username=%q ok=%t", username, ok)
	}
}

func TestUpdatePodsRejectsOversizedInputBeforeOpeningDatabase(t *testing.T) {
	pods := make([]integration.Pod, maxPodReconciliationItems+1)
	if err := (&Store{}).UpdatePods(context.Background(), pods, `^jupyter-(?P<username>.+)$`); err == nil {
		t.Fatal("oversized Pod reconciliation input was accepted")
	}
}

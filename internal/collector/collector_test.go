package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

func TestNormalizeLLMLabels(t *testing.T) {
	points := []integration.MetricPoint{{Labels: map[string]string{"kubernetes_pod_name": "jupyter-user01", "http_route": "/v1/chat/completions", "code": "200", "served_model": "llama", "authorization": "Bearer must-not-be-copied"}}}
	got := normalizeLLMLabels(points, map[string]string{"pod": "kubernetes_pod_name", "path": "http_route", "status": "code", "model": "served_model", "hub": "authorization"})
	labels := got[0].Labels
	for key, want := range map[string]string{"pod": "jupyter-user01", "path": "/v1/chat/completions", "status": "200", "model": "llama"} {
		if labels[key] != want {
			t.Fatalf("%s=%q want %q", key, labels[key], want)
		}
	}
	if labels["hub"] != "" {
		t.Fatal("sensitive remote label must not be copied")
	}
	for _, key := range []string{"authorization", "kubernetes_pod_name", "http_route", "code", "served_model"} {
		if _, exists := labels[key]; exists {
			t.Fatalf("raw label %q must be discarded", key)
		}
	}
}

func TestLLMScanCadenceDoesNotOverlapDefaultPromQLWindow(t *testing.T) {
	c := &Collector{}
	now := time.Date(2026, 9, 2, 12, 0, 29, 900, time.UTC)
	scanAt := llmEvaluationTime(now)
	if !scanAt.Equal(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("evaluation time was not aligned to a UTC minute: %s", scanAt)
	}
	if !c.llmScanDue(scanAt) {
		t.Fatal("first scan should run")
	}
	if c.llmScanDue(llmEvaluationTime(now.Add(20 * time.Second))) {
		t.Fatal("30-second base tick would overlap the one-minute increase window")
	}
	if !c.llmScanDue(llmEvaluationTime(now.Add(time.Minute))) {
		t.Fatal("one-minute counter window should run")
	}
}

func TestPruneRetriesSoonAfterFailure(t *testing.T) {
	c := &Collector{}
	start := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	if !c.pruneDue(start) {
		t.Fatal("first prune should run")
	}
	if c.pruneDue(start.Add(time.Minute)) {
		t.Fatal("a successful prune must not repeat on the next base tick")
	}
	c.pruneFailed()
	if c.pruneDue(start.Add(pruneRetryInterval - time.Minute)) {
		t.Fatal("a failed prune must not retry on every base tick")
	}
	if !c.pruneDue(start.Add(pruneRetryInterval)) {
		t.Fatal("a failed prune must retry well before the next daily cycle")
	}
	if c.pruneDue(start.Add(pruneRetryInterval + pruneRetryInterval)) {
		t.Fatal("a successful retry must restore the daily cadence")
	}
	if !c.pruneDue(start.Add(pruneRetryInterval + pruneInterval)) {
		t.Fatal("the daily cycle should run again")
	}
}

func TestRetentionReadableOnlyToleratesMissingSettings(t *testing.T) {
	if !retentionReadable(nil) {
		t.Fatal("a successful read is usable")
	}
	if !retentionReadable(fmt.Errorf("read settings: %w", store.ErrNotFound)) {
		t.Fatal("an unset retention window falls back to the built-in default")
	}
	if retentionReadable(errors.New("connection refused")) {
		t.Fatal("pruning with the default window after a failed read would delete samples the operator kept")
	}
}

func TestHealthWriteContextSurvivesProviderCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatalf("independent health context inherited cancellation: %v", ctx.Err())
	default:
	}
}

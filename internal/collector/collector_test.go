package collector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
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

func TestHubProbesRunUnderAConcurrencyCap(t *testing.T) {
	c := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	running, peak, completed := 0, 0, 0
	release := make(chan struct{})
	for range hubCollectConcurrency * 3 {
		c.goHub(context.Background(), func() {
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			<-release
			mu.Lock()
			running--
			completed++
			mu.Unlock()
		})
	}
	// Hold every admitted probe open so the cap is observed at its ceiling
	// rather than at whatever the scheduler happened to overlap.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		admitted := running
		mu.Unlock()
		if admitted >= hubCollectConcurrency {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d probes started, want the full cap of %d", admitted, hubCollectConcurrency)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	c.waitForHubs()
	mu.Lock()
	defer mu.Unlock()
	if peak > hubCollectConcurrency {
		t.Fatalf("%d hub probes ran at once, cap is %d", peak, hubCollectConcurrency)
	}
	if completed != hubCollectConcurrency*3 {
		t.Fatalf("completed %d probes, want every queued one to run", completed)
	}
}

func TestWaitForHubsBlocksUntilInFlightProbesFinish(t *testing.T) {
	c := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var wrote atomic.Bool
	c.goHub(ctx, func() {
		close(started)
		time.Sleep(50 * time.Millisecond)
		// Stands in for updateHubHealth, which writes on a context detached
		// from the collector's precisely so cancellation cannot suppress it.
		wrote.Store(true)
	})
	<-started
	cancel()
	c.waitForHubs()
	if !wrote.Load() {
		t.Fatal("shutdown returned before an in-flight hub probe stored its health")
	}
}

func TestQueuedHubProbesAreDroppedAfterCancellation(t *testing.T) {
	c := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Occupy every slot without releasing it, so the next probe can only be
	// admitted once a running one finishes — which never happens here.
	for range hubCollectConcurrency {
		c.hubSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var queued atomic.Int64
	c.goHub(ctx, func() { queued.Add(1) })
	c.waitForHubs()
	if queued.Load() != 0 {
		t.Fatal("a probe queued behind the cap started work after shutdown began")
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

func TestPrometheusQueriesResolveGPUSetFromOneFeatureSnapshot(t *testing.T) {
	defaults := prometheusQueries(nil, false)
	if len(defaults) != 2 || defaults["cpu_cores"] == "" || defaults["memory_bytes"] == "" {
		t.Fatalf("unconfigured cycle lost its base metrics: %#v", defaults)
	}
	configured := map[string]string{
		"cpu_cores":               `up`,
		"gpu_utilization":         `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`,
		"accelerator_utilization": `sum(DCGM_FI_DEV_FB_USED) by (pod)`,
		"board_temperature":       `avg(nvidia_smi_temperature) by (pod)`,
	}
	off := prometheusQueries(configured, false)
	if len(off) != 1 || off["cpu_cores"] != `up` {
		t.Fatalf("disabled GPU monitoring left GPU metrics in the cycle: %#v", off)
	}
	on := prometheusQueries(configured, true)
	for name, want := range configured {
		if on[name] != want {
			t.Fatalf("configured query %q was replaced: %#v", name, on)
		}
	}
	for _, name := range []string{"gpu_count", "vram_bytes"} {
		if on[name] == "" {
			t.Fatalf("default GPU query %q was not added: %#v", name, on)
		}
	}
	if len(configured) != 4 {
		t.Fatalf("caller configuration was mutated: %#v", configured)
	}
}

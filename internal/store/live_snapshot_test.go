package store

import (
	"testing"
	"time"
)

func TestGPUMetricsExpectedDoesNotMarkCPUOnlySessionsStale(t *testing.T) {
	zero, one := 0, 1
	for _, test := range []struct {
		name    string
		enabled bool
		count   *int
		detail  map[string]any
		want    bool
	}{
		{name: "GPU monitoring disabled", enabled: false, count: &one, detail: map[string]any{"gpu_utilization": 50.0}, want: false},
		{name: "CPU-only unknown allocation", enabled: true, count: nil, detail: map[string]any{}, want: false},
		{name: "CPU-only explicit zero", enabled: true, count: &zero, detail: map[string]any{}, want: false},
		{name: "allocated GPU", enabled: true, count: &one, detail: map[string]any{}, want: true},
		{name: "observed GPU metric", enabled: true, count: nil, detail: map[string]any{"gpu_utilization": 0.0}, want: true},
		{name: "observed GPU timestamp", enabled: true, count: nil, detail: map[string]any{"gpu_sampled_at": "2026-09-03T00:00:00Z"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := gpuMetricsExpected(test.enabled, test.count, test.detail); got != test.want {
				t.Fatalf("gpuMetricsExpected=%t want %t", got, test.want)
			}
		})
	}
}

func TestMetricSampleFreshRequiresTheRequestedMetricTimestampForPrometheus(t *testing.T) {
	now := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	detail := map[string]any{
		"resource_sampled_at": fresh,
		"memory_sampled_at":   fresh,
	}
	if metricSampleFresh(now, detail, "cpu_sampled_at", now, true) {
		t.Fatal("a memory/resource timestamp must not make a missing CPU metric fresh")
	}
	if !metricSampleFresh(now, detail, "memory_sampled_at", time.Time{}, true) {
		t.Fatal("the requested fresh metric timestamp was rejected")
	}
	if !metricSampleFresh(now, detail, "cpu_sampled_at", now, false) {
		t.Fatal("Hub-only mode should retain the server snapshot fallback")
	}
}

func TestTopLiveUsersIncludesAndSortsRuntime(t *testing.T) {
	items := []map[string]any{
		{"username": "short", "runtime_seconds": float64(1800)},
		{"username": "long", "runtime_seconds": float64(3600)},
		{"username": "long", "runtime_seconds": float64(7200)},
	}
	top := topLiveUsers(items)
	if len(top) != 2 || top[0]["username"] != "long" || top[0]["runtime_seconds"] != float64(10800) || top[0]["usage_hours"] != float64(3) {
		t.Fatalf("live runtime aggregation missing or unsorted: %#v", top)
	}
}

func TestBuildLiveHubCardsIncludesRealtimeCounts(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-10 * time.Minute)
	hubs := []Hub{
		{ID: 1, Name: "업무망", Network: "prod", Status: "healthy", Enabled: true, LastSeenAt: &now, CollectIntervalSeconds: 60},
		{ID: 2, Name: "개발망", Network: "dev", Status: "healthy", Enabled: true, LastSeenAt: &old, CollectIntervalSeconds: 60},
		{ID: 3, Name: "수집중지망", Network: "disabled", Status: "healthy", Enabled: false, LastSeenAt: &now, CollectIntervalSeconds: 60},
	}
	sessions := []map[string]any{
		{"hub_id": int64(1), "username": "user01"},
		{"hub_id": int64(1), "username": "user01"},
		{"hub_id": int64(1), "username": "user02"},
		{"hub_id": int64(3), "username": "must-not-count"},
	}
	cards := buildLiveHubCards(hubs, sessions, map[int64]int64{1: 10, 2: 5, 3: 99}, now)
	if len(cards) != 3 || cards[0]["running_servers"] != 3 || cards[0]["active_users"] != 2 || cards[0]["total_users"] != int64(10) {
		t.Fatalf("hub realtime counts missing: %#v", cards)
	}
	if cards[1]["running_servers"] != 0 || cards[1]["active_users"] != 0 {
		t.Fatalf("empty hub counts are incorrect: %#v", cards[1])
	}
	if cards[0]["stale"] != false || cards[1]["stale"] != true {
		t.Fatalf("hub snapshot freshness is incorrect: %#v", cards)
	}
	if cards[2]["running_servers"] != 0 || cards[2]["active_users"] != 0 || cards[2]["total_users"] != int64(0) || cards[2]["status"] != "disabled" {
		t.Fatalf("disabled Hub leaked into current counts: %#v", cards[2])
	}
}

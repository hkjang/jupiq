package store

import (
	"strings"
	"testing"
)

func TestUsageQueriesIncludeAllActivitySources(t *testing.T) {
	for _, source := range []string{"metric_samples", "audit_logs", "managed_users", "servers"} {
		if !strings.Contains(usageActiveCountSQL, source) {
			t.Fatalf("active count query missing %s", source)
		}
	}
	for _, measure := range []string{"runtime_seconds", "cpu_average", "memory_average", "gpu_average", "login_count"} {
		if !strings.Contains(usageTopUsersSQL, measure) {
			t.Fatalf("top users query missing %s", measure)
		}
	}
	if !strings.Contains(usageTopUsersSQL, "runtime_sample_count") || !strings.Contains(usageTopUsersSQL, "CASE WHEN") {
		t.Fatal("top user runtime must use collected metrics with server interval fallback")
	}
	if strings.Contains(usageTopUsersSQL, "runtime_seconds,0)+COALESCE(server_usage.runtime_seconds") {
		t.Fatal("top user runtime must not add metric and server runtime")
	}
}

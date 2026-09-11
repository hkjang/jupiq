package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

// resetRollupPosition makes a test independent of whatever the rollup has
// already consumed, so seeded history is always inside the window it re-derives.
func resetRollupPosition(t *testing.T, s *Store, ctx context.Context) {
	t.Helper()
	if _, err := s.Pool.Exec(ctx, `UPDATE resource_usage_rollup_state SET rolled_up_through=to_timestamp(0) WHERE id`); err != nil {
		t.Fatal(err)
	}
}

func openUsageTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database, ctx
}

// seedSamples writes one metric series as if the collector had sampled it every
// `every` for `count` points starting at `start`.
func seedSamples(t *testing.T, s *Store, ctx context.Context, username, hub, pod, metric string, value float64, start time.Time, every time.Duration, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		labels := fmt.Sprintf(`{"username":%q,"hub":%q,"pod":%q}`, username, hub, pod)
		if _, err := s.Pool.Exec(ctx, `INSERT INTO metric_samples(source,metric_name,metric_kind,labels,value,sampled_at) VALUES('prometheus',$1,'generic',$2::jsonb,$3,$4)`,
			metric, labels, value, start.Add(time.Duration(i)*every)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResourceUsageRollupIntegratesConsumptionNotAverageIntegration(t *testing.T) {
	database, ctx := openUsageTestStore(t)
	marker := fmt.Sprintf("rollup-%d", time.Now().UnixNano())
	heavy, light := marker+"-heavy", marker+"-light"
	hub := marker + "-hub"
	// Both users hold exactly one core, so their averages are identical. Only
	// the duration differs: this is the distinction the old average could not
	// express and the whole reason consumption is integrated.
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	seedSamples(t, database, ctx, heavy, hub, heavy+"-pod", "cpu_cores", 1.0, base, 30*time.Second, 120) // one hour
	seedSamples(t, database, ctx, light, hub, light+"-pod", "cpu_cores", 1.0, base, 30*time.Second, 10)  // five minutes
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE labels->>'username' LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE username LIKE $1`, marker+"%")
	})

	resetRollupPosition(t, database, ctx)
	if _, err := database.RollUpResourceUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	read := func(username string) float64 {
		var seconds float64
		if err := database.Pool.QueryRow(ctx, `SELECT COALESCE(sum(cpu_core_seconds),0) FROM resource_usage_hourly WHERE username=$1`, username).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	heavySeconds, lightSeconds := read(heavy), read(light)
	// 120 samples 30s apart: 119 spans of 30s plus a final span that inherits
	// the 30s cadence, so one core held for an hour is 3600 core-seconds.
	if heavySeconds < 3560 || heavySeconds > 3640 {
		t.Fatalf("one core held for an hour = %.0f core-seconds, want ~3600", heavySeconds)
	}
	if lightSeconds < 250 || lightSeconds > 450 {
		t.Fatalf("one core held for five minutes = %.0f core-seconds, want ~300", lightSeconds)
	}
	if heavySeconds <= lightSeconds*5 {
		t.Fatalf("consumption failed to separate the two users: heavy=%.0f light=%.0f", heavySeconds, lightSeconds)
	}
}

func TestResourceUsageRollupCapsGapsAndIsIdempotentIntegration(t *testing.T) {
	database, ctx := openUsageTestStore(t)
	marker := fmt.Sprintf("rollup-gap-%d", time.Now().UnixNano())
	hub := marker + "-hub"
	base := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Hour)
	// Two samples an hour apart: the collector was down in between. Integrating
	// naively would credit a full hour of consumption to a single observation.
	seedSamples(t, database, ctx, marker, hub, marker+"-pod", "cpu_cores", 2.0, base, time.Hour, 2)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE labels->>'username' LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE username LIKE $1`, marker+"%")
	})

	resetRollupPosition(t, database, ctx)
	if _, err := database.RollUpResourceUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	total := func() float64 {
		var seconds float64
		if err := database.Pool.QueryRow(ctx, `SELECT COALESCE(sum(cpu_core_seconds),0) FROM resource_usage_hourly WHERE username=$1`, marker).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	first := total()
	// Each of the two samples may represent at most maxSampleInterval.
	ceiling := 2 * 2.0 * maxSampleInterval.Seconds()
	if first <= 0 || first > ceiling {
		t.Fatalf("gap integrated to %.0f core-seconds, want >0 and <=%.0f", first, ceiling)
	}

	// Running again must not double-count: the rollup is an upsert, and it
	// re-derives recent buckets on every pass.
	resetRollupPosition(t, database, ctx)
	if _, err := database.RollUpResourceUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if second := total(); math.Abs(second-first) > 0.001 {
		t.Fatalf("second rollup changed consumption: %.3f then %.3f", first, second)
	}
}

func TestResourceConsumptionSurvivesRawSamplePruneIntegration(t *testing.T) {
	database, ctx := openUsageTestStore(t)
	marker := fmt.Sprintf("rollup-prune-%d", time.Now().UnixNano())
	hub := marker + "-hub"
	// Samples old enough that the raw-sample retention will remove them.
	base := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(time.Hour)
	seedSamples(t, database, ctx, marker, hub, marker+"-pod", "cpu_cores", 4.0, base, 30*time.Second, 120)
	seedSamples(t, database, ctx, marker, hub, marker+"-pod", "memory_bytes", 2<<30, base, 30*time.Second, 120)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE labels->>'username' LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE username LIKE $1`, marker+"%")
	})

	resetRollupPosition(t, database, ctx)
	if _, err := database.RollUpResourceUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Raw samples now age out on the 30-day window the rollup was built to outlive.
	if err := database.PruneMetrics(ctx, 30, 30); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples WHERE labels->>'username'=$1`, marker).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d raw samples survived the prune, the test cannot prove durability", remaining)
	}

	from, to := base.Add(-time.Hour), base.Add(48*time.Hour)
	items, err := database.ResourceConsumption(ctx, from, to, "user", 100)
	if err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	for _, item := range items {
		if item["group"] == marker {
			found = item
		}
	}
	if found == nil {
		t.Fatal("consumption disappeared with the raw samples it was derived from")
	}
	// Four cores held for an hour is four core-hours; 2 GiB for an hour is 2 GB-hours.
	if hours := found["cpu_core_hours"].(float64); hours < 3.9 || hours > 4.1 {
		t.Fatalf("cpu_core_hours=%.3f, want ~4", hours)
	}
	if hours := found["memory_gb_hours"].(float64); hours < 1.9 || hours > 2.1 {
		t.Fatalf("memory_gb_hours=%.3f, want ~2", hours)
	}

	// The consumption window is independent of the raw window.
	if err := database.PruneResourceUsage(ctx, 365); err != nil {
		t.Fatal(err)
	}
	after, err := database.ResourceConsumption(ctx, from, to, "user", 100)
	if err != nil {
		t.Fatal(err)
	}
	still := false
	for _, item := range after {
		if item["group"] == marker {
			still = true
		}
	}
	if !still {
		t.Fatal("a 365-day usage window deleted 40-day-old consumption")
	}
}

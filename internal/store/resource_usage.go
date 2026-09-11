package store

import (
	"context"
	"fmt"
	"time"
)

// maxSampleInterval bounds how long one sample is allowed to represent. The
// collector samples every 30 seconds, so a healthy series needs far less than
// this; the cap is what stops a collector outage, a Prometheus gap or a pod
// that vanished mid-bucket from being integrated as if the last observed value
// had held for the whole gap. Consumption is under-counted across an outage
// rather than over-counted, which is the safe direction for quota and cost.
const maxSampleInterval = 150 * time.Second

// rollupLateWindow re-derives buckets that were already written. Samples arrive
// with the timestamp Prometheus reported, so a slow scrape can land in a bucket
// the rollup has already passed; recomputing the recent past absorbs that
// instead of losing it. The rollup is an upsert, so repeating a bucket is free.
const rollupLateWindow = 3 * time.Hour

// ResourceUsageBucket is one hour of one user's consumption on one hub.
type ResourceUsageBucket struct {
	Bucket            time.Time `json:"bucket"`
	Username          string    `json:"username"`
	HubName           string    `json:"hub_name"`
	Network           string    `json:"network"`
	Department        string    `json:"department"`
	CPUCoreSeconds    float64   `json:"cpu_core_seconds"`
	MemoryByteSeconds float64   `json:"memory_byte_seconds"`
	GPUSeconds        float64   `json:"gpu_seconds"`
	CPUPeakCores      float64   `json:"cpu_peak_cores"`
	MemoryPeakBytes   float64   `json:"memory_peak_bytes"`
	CoveredSeconds    float64   `json:"covered_seconds"`
	RuntimeSeconds    float64   `json:"runtime_seconds"`
	SampleCount       int       `json:"sample_count"`
}

// resourceUsageRollupSQL turns raw samples into hourly consumption.
//
// Each sample represents the span until the next sample of the same series -
// the same user, hub, pod and metric - capped at maxSampleInterval. Multiplying
// that span by the sampled rate and summing gives core-seconds and
// byte-seconds. A span is credited to the bucket holding its own sample, so a
// span crossing an hour boundary leans into the earlier bucket; totals over any
// range are exact, only the split between two adjacent buckets can shift by at
// most one sampling interval.
//
// Runtime comes from server_sessions instead of samples, so a bucket still
// records that the user was running even when Prometheus was unreachable, and
// covered_seconds vs runtime_seconds shows how much of that runtime was
// actually observed.
const resourceUsageRollupSQL = `
WITH spans AS (
    SELECT date_trunc('hour', m.sampled_at) AS bucket,
           m.labels->>'username' AS username,
           COALESCE(m.labels->>'hub', '') AS hub_name,
           COALESCE(m.labels->>'network', '') AS network,
           COALESCE(m.labels->>'department', '') AS department,
           m.metric_name,
           m.value,
           LEAST(
               COALESCE(
                   EXTRACT(EPOCH FROM (
                       LEAD(m.sampled_at) OVER series - m.sampled_at
                   )),
                   -- The final sample of a series has no successor to measure
                   -- against. Crediting it the cap would add a phantom span to
                   -- every pod that ever stopped, so it inherits the cadence
                   -- the series was actually sampled at.
                   EXTRACT(EPOCH FROM (
                       m.sampled_at - LAG(m.sampled_at) OVER series
                   )),
                   $3::double precision
               ),
               $3::double precision
           ) AS span_seconds
    FROM metric_samples m
    WHERE m.sampled_at >= $1 AND m.sampled_at < $2
      AND NULLIF(m.labels->>'username', '') IS NOT NULL
      AND m.metric_name IN ('cpu_cores','cpu','cpu_usage','memory_bytes','memory','memory_usage','gpu_count','gpu')
    WINDOW series AS (
        PARTITION BY m.labels->>'username',
                     COALESCE(m.labels->>'hub', ''),
                     COALESCE(m.labels->>'pod', m.labels->>'pod_name', ''),
                     m.metric_name
        ORDER BY m.sampled_at
    )
), integrated AS (
    SELECT bucket, username, hub_name,
           max(network) AS network,
           max(department) AS department,
           COALESCE(sum(value * span_seconds) FILTER (WHERE metric_name IN ('cpu_cores','cpu','cpu_usage')), 0) AS cpu_core_seconds,
           COALESCE(sum(value * span_seconds) FILTER (WHERE metric_name IN ('memory_bytes','memory','memory_usage')), 0) AS memory_byte_seconds,
           COALESCE(sum(value * span_seconds) FILTER (WHERE metric_name IN ('gpu_count','gpu')), 0) AS gpu_seconds,
           COALESCE(max(value) FILTER (WHERE metric_name IN ('cpu_cores','cpu','cpu_usage')), 0) AS cpu_peak_cores,
           COALESCE(max(value) FILTER (WHERE metric_name IN ('memory_bytes','memory','memory_usage')), 0) AS memory_peak_bytes,
           -- Coverage is measured on one metric family, not summed across them,
           -- so observing CPU and memory for the same second counts once.
           COALESCE(sum(span_seconds) FILTER (WHERE metric_name IN ('cpu_cores','cpu','cpu_usage')), 0) AS covered_seconds,
           count(*) AS sample_count
    FROM spans
    GROUP BY bucket, username, hub_name
), session_runtime AS (
    SELECT b.bucket,
           ss.username,
           COALESCE(h.name, '') AS hub_name,
           COALESCE(sum(GREATEST(0, EXTRACT(EPOCH FROM (
               LEAST(COALESCE(ss.ended_at, $2::timestamptz), b.bucket + interval '1 hour', $2::timestamptz)
               - GREATEST(ss.started_at, b.bucket, $1::timestamptz)
           )))), 0) AS runtime_seconds
    FROM generate_series(date_trunc('hour', $1::timestamptz),
                         date_trunc('hour', $2::timestamptz),
                         interval '1 hour') AS b(bucket)
    JOIN server_sessions ss
      ON ss.started_at < LEAST(b.bucket + interval '1 hour', $2::timestamptz)
     AND COALESCE(ss.ended_at, $2::timestamptz) > GREATEST(b.bucket, $1::timestamptz)
    LEFT JOIN hubs h ON h.id = ss.hub_id
    GROUP BY b.bucket, ss.username, COALESCE(h.name, '')
), merged AS (
    SELECT COALESCE(i.bucket, r.bucket) AS bucket,
           COALESCE(i.username, r.username) AS username,
           COALESCE(i.hub_name, r.hub_name) AS hub_name,
           COALESCE(i.network, '') AS network,
           COALESCE(i.department, '') AS department,
           COALESCE(i.cpu_core_seconds, 0) AS cpu_core_seconds,
           COALESCE(i.memory_byte_seconds, 0) AS memory_byte_seconds,
           COALESCE(i.gpu_seconds, 0) AS gpu_seconds,
           COALESCE(i.cpu_peak_cores, 0) AS cpu_peak_cores,
           COALESCE(i.memory_peak_bytes, 0) AS memory_peak_bytes,
           COALESCE(i.covered_seconds, 0) AS covered_seconds,
           COALESCE(i.sample_count, 0) AS sample_count,
           COALESCE(r.runtime_seconds, 0) AS runtime_seconds
    FROM integrated i
    FULL OUTER JOIN session_runtime r
      ON r.bucket = i.bucket AND r.username = i.username AND r.hub_name = i.hub_name
)
INSERT INTO resource_usage_hourly AS target (
    bucket, username, hub_name, hub_id, network, department,
    cpu_core_seconds, memory_byte_seconds, gpu_seconds,
    cpu_peak_cores, memory_peak_bytes, covered_seconds, sample_count, runtime_seconds, updated_at
)
SELECT m.bucket, m.username, m.hub_name,
       (SELECT h.id FROM hubs h WHERE h.name = m.hub_name ORDER BY h.id LIMIT 1),
       m.network, m.department,
       m.cpu_core_seconds, m.memory_byte_seconds, m.gpu_seconds,
       m.cpu_peak_cores, m.memory_peak_bytes, m.covered_seconds, m.sample_count, m.runtime_seconds, now()
FROM merged m
WHERE m.username IS NOT NULL AND m.username <> ''
ON CONFLICT (bucket, username, hub_name) DO UPDATE SET
    hub_id = EXCLUDED.hub_id,
    network = CASE WHEN EXCLUDED.network <> '' THEN EXCLUDED.network ELSE target.network END,
    department = CASE WHEN EXCLUDED.department <> '' THEN EXCLUDED.department ELSE target.department END,
    cpu_core_seconds = EXCLUDED.cpu_core_seconds,
    memory_byte_seconds = EXCLUDED.memory_byte_seconds,
    gpu_seconds = EXCLUDED.gpu_seconds,
    cpu_peak_cores = EXCLUDED.cpu_peak_cores,
    memory_peak_bytes = EXCLUDED.memory_peak_bytes,
    covered_seconds = EXCLUDED.covered_seconds,
    sample_count = EXCLUDED.sample_count,
    runtime_seconds = EXCLUDED.runtime_seconds,
    updated_at = now()`

// RollUpResourceUsage integrates raw samples into hourly consumption buckets up
// to the end of the last completed hour, and returns how far it advanced. The
// in-progress hour is deliberately left out: it would be rewritten on every run
// and read as complete in the meantime.
func (s *Store) RollUpResourceUsage(ctx context.Context, now time.Time) (time.Time, error) {
	through := now.UTC().Truncate(time.Hour)
	var from time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT rolled_up_through FROM resource_usage_rollup_state WHERE id`).Scan(&from); err != nil {
		return time.Time{}, err
	}
	// Re-derive the recent past so late-arriving samples are not stranded, and
	// bound the first run after an upgrade so it cannot scan all of history.
	start := from.Add(-rollupLateWindow)
	if earliest := through.Add(-rollupBackfillLimit); start.Before(earliest) {
		start = earliest
	}
	start = start.UTC().Truncate(time.Hour)
	if !start.Before(through) {
		return from, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, resourceUsageRollupSQL, start, through, maxSampleInterval.Seconds()); err != nil {
		return time.Time{}, fmt.Errorf("roll up resource usage: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE resource_usage_rollup_state SET rolled_up_through=$1, updated_at=now() WHERE id`, through); err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return through, nil
}

// rollupBackfillLimit caps the first rollup after an upgrade, whose recorded
// position is the epoch, to the window raw samples can still exist in.
const rollupBackfillLimit = 45 * 24 * time.Hour

// RolledUpThrough reports the end of the last hour the rollup has consumed.
func (s *Store) RolledUpThrough(ctx context.Context) (time.Time, error) {
	var through time.Time
	err := s.Pool.QueryRow(ctx, `SELECT rolled_up_through FROM resource_usage_rollup_state WHERE id`).Scan(&through)
	return through, err
}

// ResourceConsumption sums the hourly buckets into per-group totals. Unlike the
// sample averages, these answer how much was consumed rather than how hard the
// pods were working while they ran, and they keep working after the raw samples
// behind them have been pruned.
func (s *Store) ResourceConsumption(ctx context.Context, from, to time.Time, groupBy string, limit int) ([]map[string]any, error) {
	// The grouping column is chosen from this table, never interpolated from
	// the request, so a group_by value can never reach the query.
	columns := map[string]string{
		"user":       "username",
		"hub":        "hub_name",
		"network":    "network",
		"department": "department",
	}
	column, ok := columns[groupBy]
	if !ok {
		groupBy, column = "user", "username"
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var gpuEnabled struct {
		GPUMonitoring bool `json:"gpu_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &gpuEnabled)
	rows, err := s.Pool.Query(ctx, `
		SELECT COALESCE(NULLIF(`+column+`,''),'unknown') AS grouping,
		       COALESCE(sum(cpu_core_seconds),0),
		       COALESCE(sum(memory_byte_seconds),0),
		       COALESCE(sum(gpu_seconds),0),
		       COALESCE(max(cpu_peak_cores),0),
		       COALESCE(max(memory_peak_bytes),0),
		       COALESCE(sum(runtime_seconds),0),
		       COALESCE(sum(covered_seconds),0),
		       COALESCE(sum(sample_count),0)
		FROM resource_usage_hourly
		WHERE bucket >= $1 AND bucket < $2
		GROUP BY 1
		ORDER BY 2 DESC, 7 DESC
		LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var group string
		var cpuSeconds, memorySeconds, gpuSeconds, cpuPeak, memoryPeak, runtime, covered float64
		var samples int64
		if err := rows.Scan(&group, &cpuSeconds, &memorySeconds, &gpuSeconds, &cpuPeak, &memoryPeak, &runtime, &covered, &samples); err != nil {
			return nil, err
		}
		item := map[string]any{
			"group":          group,
			"group_by":       groupBy,
			"cpu_core_hours": cpuSeconds / 3600,
			// Byte-seconds are divided by both the hour and the gigabyte so the
			// figure reads as "GB held for an hour", the unit quotas are set in.
			"memory_gb_hours":     memorySeconds / 3600 / (1 << 30),
			"cpu_core_seconds":    cpuSeconds,
			"memory_byte_seconds": memorySeconds,
			"cpu_peak_cores":      cpuPeak,
			"memory_peak_bytes":   memoryPeak,
			"runtime_seconds":     runtime,
			"sample_count":        samples,
			// What share of the measured runtime actually had samples behind it.
			// A low number means the totals understate real consumption because
			// collection was down, not that the user ran less.
			"observed_ratio": observedRatio(covered, runtime),
		}
		if gpuEnabled.GPUMonitoring {
			item["gpu_hours"] = gpuSeconds / 3600
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// observedRatio reports how much of the runtime was backed by samples, clamped
// to 1 because a sample span may overhang the end of a session slightly.
func observedRatio(covered, runtime float64) float64 {
	if runtime <= 0 {
		if covered > 0 {
			return 1
		}
		return 0
	}
	ratio := covered / runtime
	if ratio > 1 {
		return 1
	}
	return ratio
}

// UserResourceConsumption returns one user's consumption split into buckets of
// the requested width, for the per-user usage view.
func (s *Store) UserResourceConsumption(ctx context.Context, username string, from, to time.Time, granularity string) ([]map[string]any, error) {
	units := map[string]string{"hour": "hour", "day": "day", "week": "week", "month": "month"}
	unit, ok := units[granularity]
	if !ok {
		unit = "day"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT date_trunc('`+unit+`',bucket) AS period,
		       COALESCE(sum(cpu_core_seconds),0),
		       COALESCE(sum(memory_byte_seconds),0),
		       COALESCE(sum(gpu_seconds),0),
		       COALESCE(max(cpu_peak_cores),0),
		       COALESCE(max(memory_peak_bytes),0),
		       COALESCE(sum(runtime_seconds),0),
		       COALESCE(sum(covered_seconds),0)
		FROM resource_usage_hourly
		WHERE username=$1 AND bucket >= $2 AND bucket < $3
		GROUP BY 1 ORDER BY 1`, username, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var period time.Time
		var cpuSeconds, memorySeconds, gpuSeconds, cpuPeak, memoryPeak, runtime, covered float64
		if err := rows.Scan(&period, &cpuSeconds, &memorySeconds, &gpuSeconds, &cpuPeak, &memoryPeak, &runtime, &covered); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"bucket":            period,
			"cpu_core_hours":    cpuSeconds / 3600,
			"memory_gb_hours":   memorySeconds / 3600 / (1 << 30),
			"gpu_hours":         gpuSeconds / 3600,
			"cpu_peak_cores":    cpuPeak,
			"memory_peak_bytes": memoryPeak,
			"runtime_seconds":   runtime,
			"observed_ratio":    observedRatio(covered, runtime),
		})
	}
	return items, rows.Err()
}

// PruneResourceUsage drops consumption buckets past their own retention, which
// is independent of and much longer than the raw sample window.
func (s *Store) PruneResourceUsage(ctx context.Context, retentionDays int) error {
	if retentionDays < 1 {
		retentionDays = 365
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE bucket < now()-($1 * interval '1 day')`, retentionDays)
	return err
}

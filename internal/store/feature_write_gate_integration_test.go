package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/secure"
)

func TestFeatureControlledWritesFailClosedIntegration(t *testing.T) {
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
	defer database.Close()
	marker := fmt.Sprintf("feature-write-gate-%d", time.Now().UnixNano())
	if err := database.Seed(ctx, marker, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	var previous []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	var hubID, managedUserID, serverID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,'https://hub.invalid',$1) RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username) VALUES($1,$2) RETURNING id`, hubID, marker).Scan(&managedUserID); err != nil {
		t.Fatal(err)
	}
	pod := "jupyter-" + marker
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,pod_name,raw) VALUES($1,$2,$3,'','running',$4,'{"sentinel":"unchanged"}') RETURNING id`, hubID, managedUserID, marker, pod).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, previous)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE pod_name=$1`, pod)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, hubID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username=$1`, marker)
	}()

	point := integration.MetricPoint{Labels: map[string]string{"pod": pod, "path": "/v1/chat/completions", "status": "200"}, Value: 7, Timestamp: time.Now().UTC()}
	assertNoControlledWrites := func(t *testing.T) {
		t.Helper()
		if err := database.SavePrometheusMetricPoints(ctx, "innocent_alias", `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`, []integration.MetricPoint{point}); err != nil {
			t.Fatal(err)
		}
		if err := database.SaveMetricPoints(ctx, marker, "gpu_count", []integration.MetricPoint{point}); err != nil {
			t.Fatal(err)
		}
		if err := database.SaveLLMMetricPoints(ctx, "calls", `^jupyter-(?P<username>.+)$`, "/v1/chat/completions", []integration.MetricPoint{point}); err != nil {
			t.Fatal(err)
		}
		if err := database.SaveLLMMetricPoints(ctx, "estimated_cost_input_tokens", `^jupyter-(?P<username>.+)$`, "/v1/chat/completions", []integration.MetricPoint{point}); err != nil {
			t.Fatal(err)
		}
		var metricCount, llmCount int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples WHERE (source=$1 OR metric_name='innocent_alias') AND labels->>'pod'=$2`, marker, pod).Scan(&metricCount); err != nil {
			t.Fatal(err)
		}
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM llm_usage_samples WHERE pod_name=$1`, pod).Scan(&llmCount); err != nil {
			t.Fatal(err)
		}
		var gpuCount *int
		var raw []byte
		if err := database.Pool.QueryRow(ctx, `SELECT gpu_count,raw FROM servers WHERE id=$1`, serverID).Scan(&gpuCount, &raw); err != nil {
			t.Fatal(err)
		}
		if metricCount != 0 || llmCount != 0 || gpuCount != nil || string(raw) != `{"sentinel": "unchanged"}` {
			t.Fatalf("OFF gate allowed controlled write: metrics=%d llm=%d gpu=%v raw=%s", metricCount, llmCount, gpuCount, raw)
		}
	}

	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value='{"gpu_monitoring":false,"llm_usage_monitoring":false}'::jsonb WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	assertNoControlledWrites(t)
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value='{"gpu_monitoring":"true","llm_usage_monitoring":1}'::jsonb WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	assertNoControlledWrites(t)

	if err := database.SaveMetricPoints(ctx, marker, "cpu_cores", []integration.MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	var genericCount int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples WHERE source=$1 AND metric_name='cpu_cores'`, marker).Scan(&genericCount); err != nil {
		t.Fatal(err)
	}
	if genericCount != 1 {
		t.Fatalf("GPU OFF gate blocked non-GPU metric: %d", genericCount)
	}
}

func TestFeatureSaveLockSerializesOffUpdateIntegration(t *testing.T) {
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
	defer database.Close()
	marker := fmt.Sprintf("feature-lock-%d", time.Now().UnixNano())
	if err := database.Seed(ctx, marker, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username=$1`, marker) }()
	var previous []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, previous)
	}()
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value='{"gpu_monitoring":true,"llm_usage_monitoring":true}'::jsonb WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}

	saveTx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = saveTx.Rollback(ctx) }()
	enabled, err := featureEnabledForSave(ctx, saveTx, "gpu_monitoring")
	if err != nil || !enabled {
		t.Fatalf("feature save lock did not observe ON: enabled=%t err=%v", enabled, err)
	}

	updateConn, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer updateConn.Release()
	var updatePID int32
	if err := updateConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&updatePID); err != nil {
		t.Fatal(err)
	}
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := updateConn.Exec(ctx, `UPDATE settings SET value='{"gpu_monitoring":false,"llm_usage_monitoring":false}'::jsonb WHERE setting_key='features'`)
		updateDone <- updateErr
	}()

	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err := database.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`, updatePID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("OFF update did not wait for the feature save transaction's shared row lock")
	}
	if err := saveTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OFF update remained blocked after save transaction committed")
	}
}

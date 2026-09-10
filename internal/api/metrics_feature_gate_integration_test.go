package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

// The metrics endpoint answers a GPU lookup made while gpu_monitoring is off
// with an empty list plus feature_enabled=false, so a client can tell the gate
// apart from a range that simply holds no samples. That distinction has to hold
// for every name the store classifies as a GPU metric, not just the few
// substrings the handler once matched on its own.
func TestMetricsReportsFeatureStateForEveryGPUAliasIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("metrics-gpu-gate-%d", time.Now().UnixNano())
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
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{gpu_monitoring}','false') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: database}
	query := func(t *testing.T, metric string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		server.metrics(response, httptest.NewRequest("GET", "/api/v1/metrics?metric="+metric, nil))
		if response.Code != 200 {
			t.Fatalf("metric=%q status=%d body=%s", metric, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	for _, metric := range []string{"gpu_utilization", "model_vram", "DCGM_FI_DEV_FB_USED", "cuda_cores", "nvidia_smi_temperature"} {
		payload := query(t, metric)
		if payload["feature_enabled"] != false {
			t.Errorf("metric=%q did not report the disabled GPU feature: %#v", metric, payload)
		}
		if items, _ := payload["data"].([]any); len(items) != 0 {
			t.Errorf("metric=%q returned GPU samples while the feature is off: %#v", metric, payload)
		}
	}
	for _, metric := range []string{"cpu_cores", ""} {
		payload := query(t, metric)
		if _, gated := payload["feature_enabled"]; gated {
			t.Errorf("metric=%q was answered by the GPU feature gate: %#v", metric, payload)
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestWarmDashboardCacheIsRedactedImmediatelyAfterFeaturesTurnOffIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("dashboard-feature-cache-%d", time.Now().UnixNano())
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
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value='{"gpu_monitoring":false,"llm_usage_monitoring":false}'::jsonb WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}

	warm := map[string]any{
		"feature_enabled": map[string]bool{"gpu_monitoring": true, "llm_usage_monitoring": true},
		"summary":         map[string]any{"active_users": 1, "gpu_usage": 2, "vram_usage": 1024},
		"live_users": []map[string]any{{
			"server_id": 1, "hub_id": 1, "username": "user01", "status": "running", "stale": false,
			"gpu_count": 2, "gpu_utilization": 90, "vram_bytes": 1024, "waste_candidate": true, "waste_score": 10,
		}},
		"sessions":   []map[string]any{{"server_id": 1, "hub_id": 1, "username": "user01", "gpu_count": 2}},
		"aggregates": map[string]any{"all": map[string]any{"gpu_count": 2, "vram_bytes": 1024, "gpu_samples": 1}},
		"gpu_users":  1,
		"gpu_waste":  []map[string]any{{"username": "user01"}},
		"llm_usage":  map[string]any{"feature_enabled": true, "data": []map[string]any{{"username": "user01", "calls": 99}}},
	}
	encoded, err := json.Marshal(warm)
	if err != nil {
		t.Fatal(err)
	}
	newWarmServer := func() *Server {
		return &Server{Store: database, liveSnapshot: append([]byte(nil), encoded...), liveCachedAt: time.Now()}
	}
	assertRedacted := func(t *testing.T, payload map[string]any) {
		t.Helper()
		serialized, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		text := string(serialized)
		for _, forbidden := range []string{"gpu_count", "gpu_utilization", "vram_bytes", "gpu_usage", "vram_usage", "gpu_waste", "waste_candidate", "waste_score", `"calls":99`} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("OFF response exposed cached %q: %s", forbidden, text)
			}
		}
		flags, _ := payload["feature_enabled"].(map[string]any)
		if flags["gpu_monitoring"] != false || flags["llm_usage_monitoring"] != false {
			t.Fatalf("OFF flags were not authoritative: %#v", flags)
		}
		llm, _ := payload["llm_usage"].(map[string]any)
		if llm["feature_enabled"] != false {
			t.Fatalf("LLM OFF shape missing: %#v", llm)
		}
	}

	t.Run("GET", func(t *testing.T) {
		server := newWarmServer()
		response := httptest.NewRecorder()
		server.dashboard(response, httptest.NewRequest("GET", "/api/v1/dashboard", nil))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, envelope.Data)
		server.invalidateLiveSnapshot()
		if len(server.liveSnapshot) != 0 || !server.liveCachedAt.IsZero() {
			t.Fatal("local live snapshot invalidation did not clear the cache")
		}
	})

	t.Run("SSE", func(t *testing.T) {
		server := newWarmServer()
		requestCtx, cancel := context.WithCancel(ctx)
		request := httptest.NewRequest("GET", "/api/v1/dashboard/live", nil).WithContext(requestCtx)
		response := &cancelOnFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		server.live(response, request)
		body := strings.TrimSpace(response.Body.String())
		if !strings.HasPrefix(body, "data: ") {
			t.Fatalf("unexpected SSE body: %s", body)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(body, "data: ")), &payload); err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, payload)
	})
}

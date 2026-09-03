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

func TestLLMUsageParametersPreserveRangeAndGrouping(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	req := httptest.NewRequest("GET", "/api/v1/llm-usage/live?range=week&group_by=model&to=2026-08-20T09%3A00%3A00%2B09%3A00", nil)
	from, to, groupBy, err := llmUsageParameters(req, now)
	if err != nil {
		t.Fatal(err)
	}
	if groupBy != "model" || !to.Equal(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)) || to.Sub(from) != 7*24*time.Hour {
		t.Fatalf("unexpected live query: from=%s to=%s group_by=%q", from, to, groupBy)
	}
}

func TestLLMUsageParametersRejectInvalidExplicitRange(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/llm-usage/live?from=not-a-time", nil)
	if _, _, _, err := llmUsageParameters(req, time.Now()); err == nil {
		t.Fatal("invalid from timestamp was accepted")
	}
}

type cancelOnFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (recorder *cancelOnFlushRecorder) Flush() {
	recorder.ResponseRecorder.Flush()
	recorder.cancel()
}

func TestLLMUsageLiveStreamsLLMContractIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	username := fmt.Sprintf("llm-live-%d", time.Now().UnixNano())
	if err := database.Seed(context.Background(), username, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	user, err := database.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.User.ID) }()
	var previous []byte
	if err := database.Pool.QueryRow(context.Background(), `SELECT value FROM settings WHERE setting_key='features'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(context.Background(), `UPDATE settings SET value=$1 WHERE setting_key='features'`, previous)
	}()
	if _, err := database.Pool.Exec(context.Background(), `UPDATE settings SET value=jsonb_set(value,'{llm_usage_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/llm-usage/live?range=week&group_by=model", nil).WithContext(requestCtx)
	recorder := &cancelOnFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	(&Server{Store: database}).llmUsageLive(recorder, req)
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: ") || strings.Contains(body, "live_users") {
		t.Fatalf("LLM SSE returned a dashboard payload: %s", body)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(body, "data: "))
	var result map[string]any
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatal(err)
	}
	if result["feature_enabled"] != true || result["group_by"] != "model" {
		t.Fatalf("LLM SSE lost feature/group contract: %#v", result)
	}
}

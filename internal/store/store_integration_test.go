package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/secure"
)

func TestUsageAndHubReconciliationIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	var hubID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,'https://hub.example.internal',$1) RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE request_id=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, hubID)
	}()
	now := time.Now().UTC()
	raw := json.RawMessage(fmt.Sprintf(`{"ready":true,"started":%q,"last_activity":%q}`, now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)))
	hub := Hub{ID: hubID}
	if err := database.SyncHubUsers(ctx, hub, []integration.JupyterUser{{Name: marker + "-one", Raw: json.RawMessage(`{}`), Servers: map[string]json.RawMessage{"": raw}}, {Name: marker + "-two", Raw: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	username := marker + "-one"
	podName := "jupyter-" + username
	if err := database.UpdatePods(ctx, []integration.Pod{{Name: podName, Node: "node-a", Labels: map[string]string{}}}, `^jupyter-(?P<username>.+)$`); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMetricPoints(ctx, marker, "cpu_cores", []integration.MetricPoint{{Labels: map[string]string{"pod": podName}, Value: .75, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	var linkedPod, metricUsername string
	if err := database.Pool.QueryRow(ctx, `SELECT pod_name FROM servers WHERE hub_id=$1 AND username=$2`, hubID, username).Scan(&linkedPod); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT labels->>'username' FROM metric_samples WHERE source=$1 AND metric_name='cpu_cores' ORDER BY id DESC LIMIT 1`, marker).Scan(&metricUsername); err != nil {
		t.Fatal(err)
	}
	if linkedPod != podName || metricUsername != username {
		t.Fatalf("pod resource mapping failed: pod=%q metric username=%q", linkedPod, metricUsername)
	}
	if err := database.SyncHubUsers(ctx, hub, []integration.JupyterUser{{Name: marker + "-one", Raw: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	var running, inactive int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='running') FROM servers WHERE hub_id=$1`, hubID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Fatalf("missing server remained running: %d", running)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_users WHERE hub_id=$1 AND active=false`, hubID).Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 1 {
		t.Fatalf("missing user not deactivated: %d", inactive)
	}
	if err := database.SyncHubUsers(ctx, hub, nil); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_users WHERE hub_id=$1 AND active`, hubID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Fatalf("empty snapshot left active users: %d", running)
	}
	if err := database.SaveMetricPoints(ctx, marker, "cpu_usage", []integration.MetricPoint{{Labels: map[string]string{"username": username, "hub": marker}, Value: .75, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordAudit(ctx, AuditEvent{ActorUsername: username, Action: "auth.login", ResourceType: "session", Result: "success", RequestID: marker}); err != nil {
		t.Fatal(err)
	}
	usage, err := database.Usage(ctx, now.Add(-24*time.Hour), now.Add(time.Minute), "hour", "user")
	if err != nil {
		t.Fatal(err)
	}
	if usage["dau"].(int) < 1 || len(usage["top_users"].([]map[string]any)) < 1 || len(usage["trend"].([]map[string]any)) < 1 {
		t.Fatalf("usage sources were not aggregated: %#v", usage)
	}
	var previous []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, previous)
	}()
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{llm_usage_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO llm_usage_samples(source,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p95_ms,input_tokens,output_tokens,total_tokens,estimated_cost,sampled_at) VALUES($1,$2,$3,$4,'model-a',10,9,1,123,100,20,120,.5,$5)`, marker, username, "jupyter-"+username, marker, now); err != nil {
		t.Fatal(err)
	}
	llm, err := database.LLMUsage(ctx, now.Add(-time.Hour), now.Add(time.Minute), "user")
	if err != nil {
		t.Fatal(err)
	}
	summary := llm["summary"].(map[string]any)
	if summary["calls"] != int64(10) || summary["success_rate"].(float64) != .9 || len(llm["breakdown"].([]map[string]any)) != 1 || len(llm["usage_trend"].([]map[string]any)) != 1 {
		t.Fatalf("LLM usage contract incomplete: %#v", llm)
	}
}

func TestTransitionResourceConcurrentIntegration(t *testing.T) {
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
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&actorID); err != nil {
		t.Fatal("integration database must be seeded: ", err)
	}
	approval, err := database.CreateResource(ctx, "approval", ResourceWrite{Name: "concurrent approval", Status: "pending", Data: json.RawMessage(`{"resource_type":"server"}`)}, actorID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM resources WHERE id=$1`, approval.ID) }()
	var claimed, executions atomic.Int32
	var wg sync.WaitGroup
	errorsCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, won, err := database.TransitionResource(ctx, "approval", approval.ID, "pending", ResourceWrite{Name: approval.Name, Status: "executing", Data: approval.Data}, actorID)
			if err != nil {
				errorsCh <- err
				return
			}
			if won {
				claimed.Add(1)
				executions.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if claimed.Load() != 1 || executions.Load() != 1 {
		t.Fatalf("atomic claim failed: claimed=%d executions=%d", claimed.Load(), executions.Load())
	}
	saved, err := database.GetResource(ctx, "approval", approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "executing" {
		t.Fatalf("status=%s", saved.Status)
	}
}

func TestAPIKeyRotationAtomicIntegration(t *testing.T) {
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
	username := fmt.Sprintf("key-rotation-%d", time.Now().UnixNano())
	var userID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) }()
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	old, _, err := database.CreateAPIKey(ctx, userID, "automation", []string{"dashboard:read"}, &expires, nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement, secret, err := database.RotateAPIKey(ctx, userID, old.ID, &expires)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == old.ID || secret == "" {
		t.Fatal("replacement key was not created")
	}
	var active, rotated int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='active'),count(*) FILTER(WHERE status='rotated') FROM api_keys WHERE user_id=$1`, userID).Scan(&active, &rotated); err != nil {
		t.Fatal(err)
	}
	if active != 1 || rotated != 1 {
		t.Fatalf("rotation was not atomic: active=%d rotated=%d", active, rotated)
	}
	if _, _, err := database.RotateAPIKey(ctx, userID, old.ID, &expires); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotated key could be rotated twice: %v", err)
	}
}

func TestUserDetail360Integration(t *testing.T) {
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

	marker := fmt.Sprintf("detail-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)
	historicalFrom := now.Truncate(24 * time.Hour).Add(-10 * 24 * time.Hour)
	historicalTo := historicalFrom.Add(48 * time.Hour)
	var userID, hubID, managedID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,email,department,auth_source) VALUES($1,'상세 사용자',$2,'AI팀','oidc') RETURNING id`, marker, marker+"@example.internal").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network,status,version) VALUES($1,'https://detail.example.internal','업무망','healthy','5.3') RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE request_id=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, hubID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	}()
	if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name,department,active,last_activity_at,raw) VALUES($1,$2,'상세 사용자','AI팀',true,$3,$4) RETURNING id`, hubID, marker, now.Add(-time.Minute), json.RawMessage(`{"prompt":"TOP_SECRET_PROMPT"}`)).Scan(&managedID); err != nil {
		t.Fatal(err)
	}
	var currentServerID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,pod_name,image,cpu_cores,memory_bytes,raw,synced_at) VALUES($1,$2,$3,'current','running',$4,$5,$6,'pytorch:latest',2,4096,$7,$5) RETURNING id`, hubID, managedID, marker, now.Add(-2*time.Hour), now.Add(-time.Minute), "jupyter-"+marker, json.RawMessage(`{"code":"TOP_SECRET_CODE"}`)).Scan(&currentServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,synced_at) VALUES($1,$2,$3,'past','stopped',$4,$5,$5)`, hubID, managedID, marker, historicalFrom.Add(12*time.Hour), historicalFrom.Add(36*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO metric_samples(source,metric_name,labels,value,sampled_at) VALUES($1,'runtime_seconds',jsonb_build_object('username',$2::text),42,$3),($1,'cpu_usage',jsonb_build_object('username',$2::text),.5,$3)`, marker, marker, now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,actor_username,action,resource_type,resource_id,result,request_id,created_at) VALUES($1,$2,'server.stop','server',($3::bigint)::text,'success',$4,$5)`, userID, marker, currentServerID, marker, now.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO llm_usage_samples(source,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p95_ms,input_tokens,output_tokens,total_tokens,estimated_cost,sampled_at,labels) VALUES($1,$2,$3,$1,'model-a',10,9,1,123,100,20,120,.5,$4,$5)`, marker, marker, "jupyter-"+marker, now.Add(-10*time.Second), json.RawMessage(`{"prompt":"TOP_SECRET_PROMPT"}`)); err != nil {
		t.Fatal(err)
	}
	var previousFeatures []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&previousFeatures); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{llm_usage_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, previousFeatures)
	}()

	detail, err := database.GetUserDetail(ctx, marker, UserDetailOptions{Page: 1, Limit: 1, IncludeLLM: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if detail.User == nil || len(detail.Hubs) != 1 || detail.Servers.CurrentTotal != 1 || len(detail.Servers.Current) != 1 || detail.Servers.Meta.Total != 1 || len(detail.Servers.History) != 1 {
		t.Fatalf("identity/server contract incomplete: %#v", detail)
	}
	if detail.Timeline.Meta.Total < 3 || len(detail.Timeline.Items) != 1 {
		t.Fatalf("timeline pagination incomplete: %#v", detail.Timeline)
	}
	day, ok := detail.Usage["day"]
	if !ok || day.RuntimeSeconds != 42 || day.SampleCount != 2 || day.CPUAverage != .5 {
		t.Fatalf("daily usage should prefer collected runtime over server fallback: %#v", day)
	}
	llmSummary, ok := detail.LLMUsage["summary"].(map[string]any)
	if !ok || llmSummary["calls"] != int64(10) || len(detail.LLMUsage["breakdown"].([]map[string]any)) != 1 {
		t.Fatalf("LLM metadata contract incomplete: %#v", detail.LLMUsage)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "TOP_SECRET_PROMPT") || strings.Contains(string(encoded), "TOP_SECRET_CODE") || strings.Contains(string(encoded), `"raw"`) {
		t.Fatalf("provider content leaked into detail response: %s", encoded)
	}
	recentUsage, err := database.Usage(ctx, now.Add(-time.Hour), now, "day", "user")
	if err != nil {
		t.Fatal(err)
	}
	var recentRuntimePoints int
	for _, point := range recentUsage["trend"].([]map[string]any) {
		if point["group"] == marker && point["metric"] == "runtime_seconds" {
			recentRuntimePoints++
			if point["average"].(float64) != 60*60 {
				t.Fatalf("recent server interval runtime=%v want 3600", point["average"])
			}
		}
	}
	if recentRuntimePoints != 1 {
		t.Fatalf("runtime metric and server interval were duplicated in trend: %d points", recentRuntimePoints)
	}
	var recentTopRuntime float64
	for _, item := range recentUsage["top_users"].([]map[string]any) {
		if item["username"] == marker {
			recentTopRuntime = item["runtime_seconds"].(float64)
			break
		}
	}
	if recentTopRuntime != 42 {
		t.Fatalf("collected runtime should override top-user server fallback: %v", recentTopRuntime)
	}

	usage, err := database.Usage(ctx, historicalFrom, historicalTo, "day", "user")
	if err != nil {
		t.Fatal(err)
	}
	var runtimeBuckets int
	var runtimeTotal, startTotal float64
	for _, point := range usage["trend"].([]map[string]any) {
		if point["group"] != marker {
			continue
		}
		switch point["metric"] {
		case "runtime_seconds":
			runtimeBuckets++
			runtime := point["average"].(float64)
			if runtime != 12*60*60 {
				t.Fatalf("multi-bucket runtime=%v want 43200: %#v", runtime, point)
			}
			runtimeTotal += runtime
		case "server_starts":
			startTotal += point["average"].(float64)
		}
	}
	if runtimeBuckets != 2 || runtimeTotal != 24*60*60 || startTotal != 1 {
		t.Fatalf("server interval was not split at bucket boundaries: buckets=%d runtime=%v starts=%v", runtimeBuckets, runtimeTotal, startTotal)
	}
	var topRuntime float64
	for _, item := range usage["top_users"].([]map[string]any) {
		if item["username"] == marker {
			topRuntime = item["runtime_seconds"].(float64)
			break
		}
	}
	if topRuntime != 24*60*60 {
		t.Fatalf("top user runtime fallback=%v want 86400", topRuntime)
	}

	beyond, err := database.GetUserDetail(ctx, marker, UserDetailOptions{Page: 10000, Limit: 1, IncludeLLM: false, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond.Timeline.Items) != 0 || beyond.Timeline.Meta.Total != detail.Timeline.Meta.Total {
		t.Fatalf("out-of-range page lost timeline total: %#v", beyond.Timeline)
	}
	if beyond.LLMUsage["included"] != false {
		t.Fatalf("include_llm=false was ignored: %#v", beyond.LLMUsage)
	}
	if _, err := database.GetUserDetail(ctx, marker+"-missing", UserDetailOptions{Page: 1, Limit: 10, Now: now}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user error=%v", err)
	}
}

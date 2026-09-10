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
	if err := database.Seed(ctx, marker+"-admin", "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := database.GetUserByUsername(ctx, marker+"-admin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, bootstrap.User.ID) }()
	var hubID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,'https://hub.example.internal',$1) RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE request_id=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE source=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE source='prometheus' AND username LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, hubID)
	}()
	now := time.Now().UTC()
	raw := json.RawMessage(fmt.Sprintf(`{"ready":true,"started":%q,"last_activity":%q}`, now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)))
	hub := Hub{ID: hubID}
	if err := database.syncHubUsers(ctx, hub, []integration.JupyterUser{{Name: marker + "-one", Roles: []string{"user", "researcher"}, Raw: json.RawMessage(`{"auth_state":{"token":"must-not-leak"}}`), Servers: map[string]json.RawMessage{"": raw}}, {Name: marker + "-two", Raw: json.RawMessage(`{}`)}}, false); err != nil {
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
	if _, err := database.Pool.Exec(ctx, `UPDATE servers SET memory_bytes=$3 WHERE hub_id=$1 AND username=$2`, hubID, username, int64(2*1024*1024*1024)); err != nil {
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
	maliciousPod := "jupyter-attacker"
	if err := database.UpdatePods(ctx, []integration.Pod{{Name: maliciousPod, Phase: "Running", Labels: map[string]string{"hub.jupyter.org/username": username}}}, `^jupyter-(?P<username>.+)$`); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT pod_name FROM servers WHERE hub_id=$1 AND username=$2`, hubID, username).Scan(&linkedPod); err != nil {
		t.Fatal(err)
	}
	if linkedPod != podName {
		t.Fatalf("mismatched Pod-name and trusted-label usernames changed attribution: %q", linkedPod)
	}
	oldPod, newPod := "jupyter-"+username+"-old", "jupyter-"+username+"-new"
	suffixedPattern := `^jupyter-(?P<username>.+)-(?:old|new)$`
	phaseCandidates := []integration.Pod{{Name: oldPod, Phase: "Failed"}, {Name: newPod, Phase: "Running"}}
	if err := database.UpdatePods(ctx, phaseCandidates, suffixedPattern); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT pod_name FROM servers WHERE hub_id=$1 AND username=$2`, hubID, username).Scan(&linkedPod); err != nil {
		t.Fatal(err)
	}
	if linkedPod != newPod {
		t.Fatalf("Running Pod was not selected over a terminal Pod: %q", linkedPod)
	}
	// Two equally current candidates are ambiguous. Neither input ordering may
	// change the already-authoritative Pod mapping.
	for _, candidates := range [][]integration.Pod{
		{{Name: oldPod, Phase: "Running"}, {Name: newPod, Phase: "Running"}},
		{{Name: newPod, Phase: "Running"}, {Name: oldPod, Phase: "Running"}},
	} {
		if err := database.UpdatePods(ctx, candidates, suffixedPattern); err != nil {
			t.Fatal(err)
		}
		if err := database.Pool.QueryRow(ctx, `SELECT pod_name FROM servers WHERE hub_id=$1 AND username=$2`, hubID, username).Scan(&linkedPod); err != nil {
			t.Fatal(err)
		}
		if linkedPod != newPod {
			t.Fatalf("ambiguous Pod candidates changed attribution based on API order: %q", linkedPod)
		}
	}
	if err := database.UpdatePods(ctx, []integration.Pod{{Name: podName, Phase: "Running"}}, `^jupyter-(?P<username>.+)$`); err != nil {
		t.Fatal(err)
	}
	managedUsers, managedPage, err := database.ListManagedUsers(ctx, 1, 20, hubID, username)
	if err != nil {
		t.Fatal(err)
	}
	if managedPage.Total != 1 || len(managedUsers) != 1 {
		t.Fatalf("managed user aggregate missing: page=%#v users=%#v", managedPage, managedUsers)
	}
	managed := managedUsers[0]
	if len(managed.Roles) != 2 || managed.Roles[0] != "user" || managed.Roles[1] != "researcher" || managed.ServerStatus != "running" || managed.ServerCount != 1 || managed.RunningServerCount != 1 {
		t.Fatalf("managed user role/server aggregate incorrect: %#v", managed)
	}
	if managed.RuntimeSeconds < 3500 || managed.CPUCores == nil || *managed.CPUCores != .75 || managed.MemoryBytes == nil || *managed.MemoryBytes != 2*1024*1024*1024 || managed.LastActivityAt == nil || managed.LastActivityAt.Before(now.Add(-time.Minute)) {
		t.Fatalf("managed user runtime/resource/activity aggregate incorrect: %#v", managed)
	}
	encodedManaged, err := json.Marshal(managed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedManaged), "must-not-leak") || strings.Contains(string(encodedManaged), "auth_state") || strings.Contains(string(encodedManaged), `"raw"`) {
		t.Fatalf("managed user response exposed raw JupyterHub metadata: %s", encodedManaged)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at) VALUES($1,$2,$3,'partial-metrics','running',$4,$5)`, hubID, managed.ID, username, now.Add(-30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	partialUsers, _, err := database.ListManagedUsers(ctx, 1, 20, hubID, username)
	if err != nil {
		t.Fatal(err)
	}
	if len(partialUsers) != 1 || partialUsers[0].ServerCount != 2 || partialUsers[0].RunningServerCount != 2 || partialUsers[0].RuntimeSeconds < 5300 || partialUsers[0].CPUCores != nil || partialUsers[0].MemoryBytes != nil {
		t.Fatalf("partial server metrics produced an unsafe aggregate: %#v", partialUsers)
	}
	if _, err := database.Pool.Exec(ctx, `DELETE FROM servers WHERE hub_id=$1 AND username=$2 AND server_name='partial-metrics'`, hubID, username); err != nil {
		t.Fatal(err)
	}
	var featureBeforeLLMWrite []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&featureBeforeLLMWrite); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, featureBeforeLLMWrite)
	}()
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{llm_usage_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	llmAt := now.Truncate(time.Minute).Add(30 * time.Second)
	llmPoint := integration.MetricPoint{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "instance": "replica-a"}, Value: .4, Timestamp: llmAt.Add(-time.Second)}
	llmPoints := []integration.MetricPoint{
		llmPoint,
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "instance": "replica-b"}, Value: .4, Timestamp: llmAt},
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "instance": "replica-c"}, Value: .4, Timestamp: llmAt},
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "500", "instance": "replica-a"}, Value: 5, Timestamp: llmAt},
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "model": "model-b", "instance": "replica-a"}, Value: 7, Timestamp: llmAt},
		{Labels: map[string]string{"pod": podName, "path": "/v1/embeddings", "status": "200", "instance": "replica-a"}, Value: 11, Timestamp: llmAt},
	}
	if err := database.SaveLLMMetricPoints(ctx, "calls", `^jupyter-(?P<username>.+)$`, "", llmPoints); err != nil {
		t.Fatal(err)
	}
	// A repeated scrape of the same minute must replace the canonical aggregate,
	// not add the already-observed counters a second time.
	if err := database.SaveLLMMetricPoints(ctx, "calls", `^jupyter-(?P<username>.+)$`, "", llmPoints); err != nil {
		t.Fatal(err)
	}
	latencyPoints := []integration.MetricPoint{
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "instance": "replica-a"}, Value: 180, Timestamp: llmAt.Add(-time.Second)},
		{Labels: map[string]string{"pod": podName, "path": "/v1/chat/completions", "status": "200", "instance": "replica-b"}, Value: 120, Timestamp: llmAt},
	}
	if err := database.SaveLLMMetricPoints(ctx, "latency_p95_ms", `^jupyter-(?P<username>.+)$`, "", latencyPoints); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE servers SET pod_name=$3 WHERE hub_id=$1 AND username=$2`, hubID, username, maliciousPod); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveLLMMetricPoints(ctx, "calls", `^jupyter-(?P<username>.+)$`, "/v1/chat/completions", []integration.MetricPoint{{Labels: map[string]string{"pod": maliciousPod, "path": "/v1/chat/completions", "status": "200"}, Value: 13, Timestamp: llmAt}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE servers SET pod_name=$3 WHERE hub_id=$1 AND username=$2`, hubID, username, podName); err != nil {
		t.Fatal(err)
	}
	misattributed := llmPoint
	misattributed.Labels = map[string]string{"pod": podName + "-named-server", "path": "/v1/chat/completions", "status": "200"}
	if err := database.SaveLLMMetricPoints(ctx, "calls", `^jupyter-(?P<username>.+)$`, "/v1/chat/completions", []integration.MetricPoint{misattributed}); err != nil {
		t.Fatal(err)
	}
	var llmRows int
	var totalCalls, successCalls, errorCalls int64
	if err := database.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(calls),0),COALESCE(sum(success_count),0),COALESCE(sum(error_count),0) FROM llm_usage_samples WHERE source='prometheus' AND username LIKE $1`, marker+"%").Scan(&llmRows, &totalCalls, &successCalls, &errorCalls); err != nil {
		t.Fatal(err)
	}
	if llmRows != 5 || totalCalls != 24 || successCalls != 19 || errorCalls != 5 {
		t.Fatalf("LLM dimensions were overwritten or merged incorrectly: rows=%d calls=%d success=%d errors=%d", llmRows, totalCalls, successCalls, errorCalls)
	}
	var canonicalCalls int64
	var canonicalLabels []byte
	if err := database.Pool.QueryRow(ctx, `SELECT calls,labels FROM llm_usage_samples WHERE source='prometheus' AND username=$1 AND metric_name='calls' AND model='' AND labels->>'status'='200' AND labels->>'path'='/v1/chat/completions'`, username).Scan(&canonicalCalls, &canonicalLabels); err != nil {
		t.Fatal(err)
	}
	if canonicalCalls != 1 || strings.Contains(string(canonicalLabels), "instance") {
		t.Fatalf("replica series were not safely aggregated: calls=%d labels=%s", canonicalCalls, canonicalLabels)
	}
	var latencyP95 float64
	if err := database.Pool.QueryRow(ctx, `SELECT latency_p95_ms FROM llm_usage_samples WHERE source='prometheus' AND username=$1 AND metric_name='latency_p95_ms'`, username).Scan(&latencyP95); err != nil {
		t.Fatal(err)
	}
	if latencyP95 != 180 {
		t.Fatalf("latency gauge aggregation must retain maximum, got %v", latencyP95)
	}
	if _, err := database.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE source='prometheus' AND username LIKE $1`, marker+"%"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, featureBeforeLLMWrite); err != nil {
		t.Fatal(err)
	}
	live, err := database.LiveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	liveSessions := live["sessions"].([]map[string]any)
	if len(liveSessions) != 1 || liveSessions[0]["server_id"] == nil {
		t.Fatalf("live session lacks stable server identity: %#v", liveSessions)
	}
	hubCards := live["hubs"].([]map[string]any)
	if len(hubCards) != 1 || hubCards[0]["running_servers"] != 1 || hubCards[0]["active_users"] != 1 || hubCards[0]["total_users"] != int64(2) {
		t.Fatalf("Hub card realtime counts missing: %#v", hubCards)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE hubs SET enabled=false WHERE id=$1`, hubID); err != nil {
		t.Fatal(err)
	}
	disabledDashboard, err := database.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if disabledDashboard["users"] != int64(0) || disabledDashboard["active_users"] != int64(0) || disabledDashboard["servers"] != int64(0) || disabledDashboard["stale"] != true {
		t.Fatalf("disabled Hub leaked into Dashboard current totals: %#v", disabledDashboard)
	}
	disabledLive, err := database.LiveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sessions := disabledLive["sessions"].([]map[string]any); len(sessions) != 0 {
		t.Fatalf("disabled Hub leaked into live sessions: %#v", sessions)
	}
	disabledCards := disabledLive["hubs"].([]map[string]any)
	if len(disabledCards) != 1 || disabledCards[0]["status"] != "disabled" || disabledCards[0]["running_servers"] != 0 || disabledCards[0]["active_users"] != 0 || disabledCards[0]["total_users"] != int64(0) {
		t.Fatalf("disabled Hub live card retained current counts: %#v", disabledCards)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE hubs SET enabled=true WHERE id=$1`, hubID); err != nil {
		t.Fatal(err)
	}
	var featureBeforeGPU []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&featureBeforeGPU); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{gpu_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMetricPoints(ctx, marker, "gpu_count", []integration.MetricPoint{{Labels: map[string]string{"pod": podName}, Value: 1.6, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMetricPoints(ctx, marker, "gpu_utilization", []integration.MetricPoint{{Labels: map[string]string{"pod": podName}, Value: 75, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	gpus, err := database.GPUUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 1 || gpus[0]["id"] == nil || gpus[0]["gpu_count"] != 2 || gpus[0]["gpu_utilization"] != float64(75) || gpus[0]["stale"] != false {
		t.Fatalf("GPU metrics did not reach the live GPU view: %#v", gpus)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, featureBeforeGPU); err != nil {
		t.Fatal(err)
	}
	servers, _, err := database.ListServers(ctx, 1, 20, hubID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].GPUCount != nil {
		t.Fatalf("GPU field remained visible after monitoring was disabled: %#v", servers)
	}
	aliasedGPU := "accelerator_" + marker
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE metric_name=$1`, aliasedGPU) }()
	if err := database.SavePrometheusMetricPoints(ctx, aliasedGPU, `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`, []integration.MetricPoint{{Labels: map[string]string{"pod": podName}, Value: 55, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	// The alias is only recognisable as GPU data through the query it was saved
	// with, so the stored metric_kind — not the name gate — is what hides it.
	if hidden, blocked, err := database.Metrics(ctx, now.Add(-time.Minute), now.Add(time.Minute), aliasedGPU, 10); err != nil || len(hidden) != 0 || blocked {
		t.Fatalf("aliased GPU metric escaped OFF gate: items=%#v blocked=%v err=%v", hidden, blocked, err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=jsonb_set(value,'{gpu_monitoring}','true') WHERE setting_key='features'`); err != nil {
		t.Fatal(err)
	}
	if absent, blocked, err := database.Metrics(ctx, now.Add(-time.Minute), now.Add(time.Minute), aliasedGPU, 10); err != nil || len(absent) != 0 || blocked {
		t.Fatalf("GPU metric collected while OFF became visible after enabling: items=%#v blocked=%v err=%v", absent, blocked, err)
	}
	if err := database.SavePrometheusMetricPoints(ctx, aliasedGPU, `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`, []integration.MetricPoint{{Labels: map[string]string{"pod": podName}, Value: 55, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	if visible, blocked, err := database.Metrics(ctx, now.Add(-time.Minute), now.Add(time.Minute), aliasedGPU, 10); err != nil || len(visible) != 1 || blocked {
		t.Fatalf("classified GPU metric saved while ON was not visible: items=%#v blocked=%v err=%v", visible, blocked, err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=$1 WHERE setting_key='features'`, featureBeforeGPU); err != nil {
		t.Fatal(err)
	}
	detailWithGPUOff, err := database.GetUserDetail(ctx, username, UserDetailOptions{Page: 1, Limit: 20, Now: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if detailWithGPUOff.Usage["day"].SampleCount != 1 {
		t.Fatalf("GPU samples affected user usage while monitoring was disabled: %#v", detailWithGPUOff.Usage["day"])
	}
	if err := database.syncHubUsers(ctx, hub, []integration.JupyterUser{{Name: marker + "-one", Raw: json.RawMessage(`{}`)}}, false); err != nil {
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
	if err := database.syncHubUsers(ctx, hub, nil, false); err != nil {
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
	if _, err := database.Pool.Exec(ctx, `INSERT INTO llm_usage_samples(source,metric_name,window_start,dimension_fingerprint,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p95_ms,input_tokens,output_tokens,total_tokens,estimated_cost,sampled_at) VALUES($1,'calls',$5,$1,$2,$3,$4,'model-a',10,9,1,123,100,20,120,.5,$5)`, marker, username, "jupyter-"+username, marker, now); err != nil {
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
	actorName := fmt.Sprintf("transition-%d", time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, actorName).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, actorID) }()
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

func TestUpdateSettingsAndSecretsRollsBackAtomicallyIntegration(t *testing.T) {
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
	username := fmt.Sprintf("settings-atomic-%d", time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, username).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, actorID) }()
	var before []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err = database.UpdateSettingsAndSecrets(ctx,
		map[string]any{"features": map[string]any{"gpu_monitoring": true, "llm_usage_monitoring": true}},
		map[string]string{"invalid\x00secret-key": "value"}, actorID,
	)
	if err == nil {
		t.Fatal("invalid secret key unexpectedly committed")
	}
	var after []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='features'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("settings committed despite secret failure: before=%s after=%s", before, after)
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
	if err := database.Seed(ctx, marker+"-admin", "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := database.GetUserByUsername(ctx, marker+"-admin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, bootstrap.User.ID) }()
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
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,pod_name,image,cpu_cores,memory_bytes,gpu_count,raw,synced_at) VALUES($1,$2,$3,'current','running',$4,$5,$6,'pytorch:latest',2,4096,2,$7,$5) RETURNING id`, hubID, managedID, marker, now.Add(-2*time.Hour), now.Add(-time.Minute), "jupyter-"+marker, json.RawMessage(`{"code":"TOP_SECRET_CODE"}`)).Scan(&currentServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO server_sessions(server_id,hub_id,username,server_name,started_at,ended_at,last_activity_at) VALUES($1,$2,$3,'current',$4,$5,$5),($1,$2,$3,'current',$6,NULL,$7)`, currentServerID, hubID, marker, now.Add(-4*time.Hour), now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	var pastServerID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,synced_at) VALUES($1,$2,$3,'past','stopped',$4,$5,$5) RETURNING id`, hubID, managedID, marker, historicalFrom.Add(12*time.Hour), historicalFrom.Add(36*time.Hour)).Scan(&pastServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO server_sessions(server_id,hub_id,username,server_name,started_at,ended_at,last_activity_at) VALUES($1,$2,$3,'past',$4,$5,$5)`, pastServerID, hubID, marker, historicalFrom.Add(12*time.Hour), historicalFrom.Add(36*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO metric_samples(source,metric_name,labels,value,sampled_at) VALUES($1,'runtime_seconds',jsonb_build_object('username',$2::text),42,$3),($1,'cpu_usage',jsonb_build_object('username',$2::text),.5,$3)`, marker, marker, now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,actor_username,action,resource_type,resource_id,result,request_id,created_at) VALUES($1,$2,'server.stop','server',($3::bigint)::text,'success',$4,$5)`, userID, marker, currentServerID, marker, now.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO llm_usage_samples(source,metric_name,window_start,dimension_fingerprint,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p95_ms,input_tokens,output_tokens,total_tokens,estimated_cost,sampled_at,labels) VALUES($1,'calls',$4,$1,$2,$3,$1,'model-a',10,9,1,123,100,20,120,.5,$4,$5)`, marker, marker, "jupyter-"+marker, now.Add(-10*time.Second), json.RawMessage(`{"prompt":"TOP_SECRET_PROMPT"}`)); err != nil {
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
	if detail.User == nil || len(detail.Hubs) != 1 || detail.Servers.CurrentTotal != 1 || len(detail.Servers.Current) != 1 || detail.Servers.Meta.Total != 2 || len(detail.Servers.History) != 1 {
		t.Fatalf("identity/server contract incomplete: %#v", detail)
	}
	if detail.Servers.Current[0].SessionID == 0 || detail.Servers.History[0].SessionID == 0 || detail.Servers.History[0].EndedAt == nil {
		t.Fatalf("session history identity/end time missing: %#v", detail.Servers)
	}
	if detail.Servers.Current[0].GPUCount != nil || detail.Servers.History[0].GPUCount != nil {
		t.Fatalf("GPU fields leaked while monitoring was disabled: %#v", detail.Servers)
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
	var recentRuntimeTotal float64
	for _, point := range recentUsage["trend"].([]map[string]any) {
		if point["group"] == marker && point["metric"] == "runtime_seconds" {
			recentRuntimePoints++
			recentRuntimeTotal += point["average"].(float64)
		}
	}
	// A one-hour range can cross a day boundary (the database groups
	// timestamptz values in its configured timezone), so validate the summed
	// interval instead of requiring exactly one daily bucket.
	if recentRuntimePoints < 1 || recentRuntimePoints > 2 || recentRuntimeTotal != 60*60 {
		t.Fatalf("runtime interval was duplicated or lost: points=%d total=%v want 3600", recentRuntimePoints, recentRuntimeTotal)
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

func TestRBACPreservesAnActiveSuperAdministratorIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("rbac-integration-%d", time.Now().UnixNano())
	if err := database.Seed(ctx, marker+"-admin", "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	admin, err := database.GetUserByUsername(ctx, marker+"-admin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%") }()

	var superRoleID int64
	if err := database.Pool.QueryRow(ctx, `SELECT id FROM roles WHERE role_key='super_admin'`).Scan(&superRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveRole(ctx, Role{ID: superRoleID, Key: "super_admin", Name: "최고 관리자", Permissions: []string{"roles:read"}}); !errors.Is(err, ErrImmutableRole) {
		t.Fatalf("super_admin wildcard mutation was not blocked: %v", err)
	}

	rows, err := database.Pool.Query(ctx, `SELECT id,active FROM users WHERE id<>$1`, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	previousActive := map[int64]bool{}
	for rows.Next() {
		var id int64
		var active bool
		if err := rows.Scan(&id, &active); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		previousActive[id] = active
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for id, active := range previousActive {
			_, _ = database.Pool.Exec(ctx, `UPDATE users SET active=$2 WHERE id=$1`, id, active)
		}
	}()
	if _, err := database.Pool.Exec(ctx, `UPDATE users SET active=false WHERE id<>$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.SetUserRoles(ctx, admin.ID, nil); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("last active super administrator removal was not blocked: %v", err)
	}

	var successorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source,active) VALUES($1,$1,'oidc',true) RETURNING id`, marker+"-successor").Scan(&successorID); err != nil {
		t.Fatal(err)
	}
	var scopedHubID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1) RETURNING id`, marker+"-scope", "https://"+marker+"-scope.internal").Scan(&scopedHubID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, scopedHubID) }()
	if err := database.SetUserRoleBindings(ctx, successorID, []RoleBinding{{RoleID: superRoleID, ScopeMode: "restricted", Scopes: []ScopeClause{{Type: "hub", Value: fmt.Sprint(scopedHubID)}}}}); err != nil {
		t.Fatalf("assign scoped wildcard role: %v", err)
	}
	if err := database.SetUserRoles(ctx, admin.ID, nil); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("scoped wildcard incorrectly counted as a surviving global administrator: %v", err)
	}
	if err := database.SetUserRoleBindings(ctx, successorID, []RoleBinding{{RoleID: superRoleID, ScopeMode: "global"}}); err != nil {
		t.Fatalf("assign successor super administrator: %v", err)
	}
	if err := database.SetUserRoles(ctx, admin.ID, nil); err != nil {
		t.Fatalf("remove wildcard after assigning a successor: %v", err)
	}
	if err := database.SetUserRoles(ctx, successorID, nil); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("successor was allowed to remove the final wildcard: %v", err)
	}
}

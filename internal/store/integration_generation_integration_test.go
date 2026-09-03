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

func TestCollectorGenerationRejectsChangedProviderAndInventoryIntegration(t *testing.T) {
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
	// This test deliberately rotates process-wide provider settings and secrets.
	// Preserve the exact pre-test state so a full integration run does not leave
	// GPU/LLM feature flags or provider credentials enabled for later tests.
	settingKeys := []string{"prometheus", "kubernetes", "llm_usage", "features"}
	previousSettings := map[string][]byte{}
	settingRows, err := database.Pool.Query(ctx, `SELECT setting_key,value FROM settings WHERE setting_key=ANY($1::text[])`, settingKeys)
	if err != nil {
		t.Fatal(err)
	}
	for settingRows.Next() {
		var key string
		var value []byte
		if err := settingRows.Scan(&key, &value); err != nil {
			settingRows.Close()
			t.Fatal(err)
		}
		previousSettings[key] = append([]byte(nil), value...)
	}
	if err := settingRows.Err(); err != nil {
		settingRows.Close()
		t.Fatal(err)
	}
	settingRows.Close()
	type savedSecret struct {
		value   []byte
		version int
	}
	secretKeys := []string{"prometheus.token", "kubernetes.token"}
	previousSecrets := map[string]savedSecret{}
	secretRows, err := database.Pool.Query(ctx, `SELECT secret_key,encrypted_value,version FROM secrets WHERE secret_key=ANY($1::text[])`, secretKeys)
	if err != nil {
		t.Fatal(err)
	}
	for secretRows.Next() {
		var key string
		var value []byte
		var version int
		if err := secretRows.Scan(&key, &value, &version); err != nil {
			secretRows.Close()
			t.Fatal(err)
		}
		previousSecrets[key] = savedSecret{value: append([]byte(nil), value...), version: version}
	}
	if err := secretRows.Err(); err != nil {
		secretRows.Close()
		t.Fatal(err)
	}
	secretRows.Close()
	t.Cleanup(func() {
		for _, key := range settingKeys {
			if value, exists := previousSettings[key]; exists {
				_, _ = database.Pool.Exec(context.Background(), `UPDATE settings SET value=$2,updated_by=NULL,updated_at=now() WHERE setting_key=$1`, key, value)
			} else {
				_, _ = database.Pool.Exec(context.Background(), `DELETE FROM settings WHERE setting_key=$1`, key)
			}
		}
		for _, key := range secretKeys {
			_, _ = database.Pool.Exec(context.Background(), `DELETE FROM secrets WHERE secret_key=$1`, key)
			if secret, exists := previousSecrets[key]; exists {
				_, _ = database.Pool.Exec(context.Background(), `INSERT INTO secrets(secret_key,encrypted_value,version) VALUES($1,$2,$3)`, key, secret.value, secret.version)
			}
		}
	})
	marker := fmt.Sprintf("provider-cas-%d", time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-actor").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM metric_samples WHERE metric_name LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM llm_usage_samples WHERE model=$1`, marker)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM hubs WHERE name LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, actorID)
	})

	configA := map[string]any{"enabled": true, "base_url": "https://prom-a.internal", "verify_tls": true, "queries": map[string]string{"cpu": "up"}}
	kubeA := map[string]any{"enabled": true, "base_url": "https://kube-a.internal", "verify_tls": true, "namespace": "default", "pod_username_regex": `^jupyter-(?P<username>.+)$`}
	llm := map[string]any{"source": "prometheus", "pod_username_regex": `^jupyter-(?P<username>.+)$`, "path_matcher": "/v1/chat/completions"}
	if err := database.UpdateSettingsAndSecrets(ctx, map[string]any{
		"prometheus": configA, "kubernetes": kubeA, "llm_usage": llm,
		"features": map[string]bool{"gpu_monitoring": true, "llm_usage_monitoring": true},
	}, map[string]string{"prometheus.token": "prom-a-token", "kubernetes.token": "kube-a-token"}, actorID); err != nil {
		t.Fatal(err)
	}
	hub, err := database.CreateHub(ctx, HubWrite{Name: marker + "-hub", BaseURL: "https://hub-a.internal", Network: marker, APIToken: "hub-a-token", CollectIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	username, pod := marker+"-user", "jupyter-"+marker+"-user"
	var managedID, serverID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name) VALUES($1,$2,$2) RETURNING id`, hub.ID, username).Scan(&managedID); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,pod_name) VALUES($1,$2,$3,'','running',$4) RETURNING id`, hub.ID, managedID, username, pod).Scan(&serverID); err != nil {
		t.Fatal(err)
	}

	var promConfig map[string]any
	_, _, promGeneration, err := database.GetSettingAndSecretGeneration(ctx, "prometheus", "prometheus.token", &promConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, llmGeneration, err := database.GetSettingsAndSecretGeneration(ctx, []string{"llm_usage", "prometheus"}, "prometheus.token")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, kubeGeneration, err := database.GetSettingsAndSecretGeneration(ctx, []string{"kubernetes", "llm_usage"}, "kubernetes.token")
	if err != nil {
		t.Fatal(err)
	}

	configB := map[string]any{"enabled": true, "base_url": "https://prom-b.internal", "verify_tls": true, "queries": map[string]string{"cpu": "up"}}
	kubeB := map[string]any{"enabled": true, "base_url": "https://kube-b.internal", "verify_tls": true, "namespace": "default", "pod_username_regex": `^jupyter-(?P<username>.+)$`}
	if err := database.UpdateSettingsAndSecrets(ctx, map[string]any{"prometheus": configB, "kubernetes": kubeB}, map[string]string{"prometheus.token": "prom-b-token", "kubernetes.token": "kube-b-token"}, actorID); err != nil {
		t.Fatal(err)
	}
	point := integration.MetricPoint{Labels: map[string]string{"pod": pod, "path": "/v1/chat/completions", "status": "200", "model": marker}, Value: 7, Timestamp: time.Now().UTC()}
	if stored, err := database.SavePrometheusMetricPointsIfCurrent(ctx, promGeneration, marker+"-provider", "up", []integration.MetricPoint{point}); err != nil || stored {
		t.Fatalf("stale Prometheus provider response was accepted: stored=%v err=%v", stored, err)
	}
	if stored, err := database.SaveLLMMetricPointsIfCurrent(ctx, llmGeneration, "calls", `^jupyter-(?P<username>.+)$`, "/v1/chat/completions", []integration.MetricPoint{point}); err != nil || stored {
		t.Fatalf("stale LLM provider response was accepted: stored=%v err=%v", stored, err)
	}
	stalePod := integration.Pod{Name: pod, Node: "stale-a-node", Phase: "Running", Labels: map[string]string{"hub.jupyter.org/username": username}}
	if stored, err := database.UpdatePodsIfCurrent(ctx, kubeGeneration, []integration.Pod{stalePod}, `^jupyter-(?P<username>.+)$`); err != nil || stored {
		t.Fatalf("stale Kubernetes provider response was accepted: stored=%v err=%v", stored, err)
	}
	var node string
	if err := database.Pool.QueryRow(ctx, `SELECT node_name FROM servers WHERE id=$1`, serverID).Scan(&node); err != nil {
		t.Fatal(err)
	}
	if node != "" {
		t.Fatalf("stale Kubernetes response changed the current server: %q", node)
	}

	// A Hub endpoint replacement retains the numeric Hub ID but represents a new
	// authority. Re-create the same username/Pod under B and prove A observations
	// cannot be attributed to it.
	_, _, inventoryGeneration, err := database.GetSettingAndSecretGeneration(ctx, "prometheus", "prometheus.token", &promConfig)
	if err != nil {
		t.Fatal(err)
	}
	hub, err = database.UpdateHub(ctx, hub.ID, HubWrite{BaseURL: "https://hub-b.internal", APIToken: "hub-b-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name) VALUES($1,$2,$2) RETURNING id`, hub.ID, username).Scan(&managedID); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,pod_name) VALUES($1,$2,$3,'','running',$4) RETURNING id`, hub.ID, managedID, username, pod).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	if stored, err := database.SavePrometheusMetricPointsIfCurrent(ctx, inventoryGeneration, marker+"-hub", "up", []integration.MetricPoint{point}); err != nil || stored {
		t.Fatalf("A Hub response was attributed to replacement B: stored=%v err=%v", stored, err)
	}

	// Hold the inventory lock so a guarded save queues after its config check.
	// Insert a new Hub/server while holding the lock; after release the save must
	// observe the phantom in the fingerprint and discard its response.
	_, _, phantomGeneration, err := database.GetSettingAndSecretGeneration(ctx, "prometheus", "prometheus.token", &promConfig)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		t.Fatal(err)
	}
	phantomResult := make(chan struct {
		stored bool
		err    error
	}, 1)
	go func() {
		stored, err := database.SavePrometheusMetricPointsIfCurrent(ctx, phantomGeneration, marker+"-phantom", "up", []integration.MetricPoint{point})
		phantomResult <- struct {
			stored bool
			err    error
		}{stored, err}
	}()
	waitForSettingsLockWaiter(t, database)
	if _, err := holder.Exec(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1)`, marker+"-phantom-hub", "https://phantom.internal"); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-phantomResult:
		if result.err != nil || result.stored {
			t.Fatalf("Hub phantom did not invalidate collection: stored=%v err=%v", result.stored, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guarded metric save did not finish after inventory lock release")
	}

	var persisted int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples WHERE metric_name LIKE $1`, marker+"%").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("stale remote samples were persisted: %d", persisted)
	}
}

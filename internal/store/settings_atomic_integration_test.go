package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/jackc/pgx/v5"
)

func TestConcurrentSettingsBindingRejectsStaleWriterIntegration(t *testing.T) {
	database, actorID, restore := openSettingsAtomicTest(t, "binding-race")
	defer restore()
	ctx := context.Background()

	configA := map[string]any{"enabled": true, "base_url": "https://prom-a.internal", "verify_tls": true}
	if err := database.UpdateSettingsAndSecrets(ctx, map[string]any{"prometheus": configA}, map[string]string{"prometheus.token": "token-a"}, actorID); err != nil {
		t.Fatal(err)
	}

	// Hold the same transaction-scoped lock as an already-running settings
	// writer. The stale request starts while A/token-a is current, but may only
	// validate after this transaction atomically commits B/token-b.
	writerTx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writerTx.Rollback(ctx) }()
	if _, err := writerTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, settingsMutationLockKey); err != nil {
		t.Fatal(err)
	}

	staleResult := make(chan error, 1)
	go func() {
		staleResult <- database.UpdateSettingsAndSecrets(ctx, map[string]any{"prometheus": configA}, nil, actorID)
	}()
	waitForSettingsLockWaiter(t, database)

	configB := map[string]any{"enabled": true, "base_url": "https://prom-b.internal", "verify_tls": true}
	rawB, err := json.Marshal(configB)
	if err != nil {
		t.Fatal(err)
	}
	encryptedB, err := database.Cipher.Encrypt([]byte("token-b"), "secret:prometheus.token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writerTx.Exec(ctx, `UPDATE settings SET value=$1,updated_by=$2,updated_at=now() WHERE setting_key='prometheus'`, rawB, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := writerTx.Exec(ctx, `INSERT INTO secrets(secret_key,encrypted_value,updated_by) VALUES('prometheus.token',$1,$2) ON CONFLICT(secret_key) DO UPDATE SET encrypted_value=EXCLUDED.encrypted_value,version=secrets.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, encryptedB, actorID); err != nil {
		t.Fatal(err)
	}
	if err := writerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-staleResult:
		if !errors.Is(err, ErrSettingsSecretReentry) {
			t.Fatalf("stale config writer was not rejected: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale settings writer did not finish after lock release")
	}

	var saved map[string]any
	secret, configured, err := database.GetSettingAndSecret(ctx, "prometheus", "prometheus.token", &saved)
	if err != nil {
		t.Fatal(err)
	}
	if !configured || saved["base_url"] != configB["base_url"] || secret != "token-b" {
		t.Fatalf("committed config/secret pair was torn: config=%#v secret=%q configured=%v", saved, secret, configured)
	}
}

func TestSettingsSecretSnapshotNeverTornIntegration(t *testing.T) {
	database, actorID, restore := openSettingsAtomicTest(t, "snapshot-race")
	defer restore()
	ctx := context.Background()

	writePair := func(index int) error {
		name := "a"
		if index%2 == 1 {
			name = "b"
		}
		return database.UpdateSettingsAndSecrets(ctx,
			map[string]any{"prometheus": map[string]any{"enabled": true, "base_url": "https://prom-" + name + ".internal", "verify_tls": true}},
			map[string]string{"prometheus.token": "token-" + name}, actorID,
		)
	}
	if err := writePair(0); err != nil {
		t.Fatal(err)
	}

	writerDone := make(chan error, 1)
	go func() {
		for index := 1; index <= 80; index++ {
			if err := writePair(index); err != nil {
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()
	for index := 0; index < 240; index++ {
		var cfg struct {
			BaseURL string `json:"base_url"`
		}
		secret, configured, err := database.GetSettingAndSecret(ctx, "prometheus", "prometheus.token", &cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !configured || !((cfg.BaseURL == "https://prom-a.internal" && secret == "token-a") || (cfg.BaseURL == "https://prom-b.internal" && secret == "token-b")) {
			t.Fatalf("observed mixed snapshot: base_url=%q secret=%q configured=%v", cfg.BaseURL, secret, configured)
		}
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
}

func TestSettingsSecretAllowlist(t *testing.T) {
	for _, key := range []string{"oidc.client_secret", "prometheus.token", "kubernetes.token", "ai.api_key", "webhook.secret"} {
		if !IsSettingsSecretKey(key) {
			t.Fatalf("documented settings secret %q was rejected", key)
		}
	}
	for _, key := range []string{"system.password", "prometheus.password", "hub.token", "oidc.other_secret", ""} {
		if IsSettingsSecretKey(key) {
			t.Fatalf("unapproved settings secret %q was accepted", key)
		}
	}
}

func waitForSettingsLockWaiter(t *testing.T, database *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := database.Pool.QueryRow(context.Background(), `
			SELECT EXISTS(
				SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database()
				  AND state='active'
				  AND wait_event_type='Lock'
				  AND wait_event='advisory'
				  AND query LIKE '%pg_advisory_xact_lock%'
			)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("settings writer never blocked on the transaction advisory lock")
}

func openSettingsAtomicTest(t *testing.T, label string) (*Store, int64, func()) {
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

	var beforeSetting []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='prometheus'`).Scan(&beforeSetting); err != nil {
		database.Close()
		t.Fatal(err)
	}
	var beforeSecret []byte
	var beforeVersion int
	secretWasPresent := true
	if err := database.Pool.QueryRow(ctx, `SELECT encrypted_value,version FROM secrets WHERE secret_key='prometheus.token'`).Scan(&beforeSecret, &beforeVersion); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			database.Close()
			t.Fatal(err)
		}
		secretWasPresent = false
	}

	username := fmt.Sprintf("settings-%s-%d", label, time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, username).Scan(&actorID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	restore := func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1,updated_by=NULL,updated_at=now() WHERE setting_key='prometheus'`, beforeSetting)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key='prometheus.token'`)
		if secretWasPresent {
			_, _ = database.Pool.Exec(ctx, `INSERT INTO secrets(secret_key,encrypted_value,version) VALUES('prometheus.token',$1,$2)`, beforeSecret, beforeVersion)
		}
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, actorID)
		database.Close()
	}
	return database, actorID, restore
}

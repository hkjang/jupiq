package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpdateHubSerializesCredentialBindingIntegration(t *testing.T) {
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

	marker := fmt.Sprintf("hub-binding-%d", time.Now().UnixNano())
	originalURL := "https://" + marker + "-a.internal"
	replacementURL := "https://" + marker + "-b.internal"
	hub, err := database.CreateHub(ctx, HubWrite{
		Name: marker, Network: "test", BaseURL: originalURL, APIToken: "token-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.DeleteHub(context.Background(), hub.ID) }()

	// Hold the Hub row so both application writers queue in a known order. With
	// the former read-before-BEGIN implementation both writers validated against
	// the old row and the second one silently produced URL A + token B.
	blocker, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var lockedID int64
	if err := blocker.QueryRow(ctx, `SELECT id FROM hubs WHERE id=$1 FOR UPDATE`, hub.ID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}

	firstName := marker + "-writer-first"
	secondName := marker + "-writer-second"
	first := openHubCredentialTestStore(t, dsn, cipher, firstName, nil)
	second := openHubCredentialTestStore(t, dsn, cipher, secondName, nil)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, updateErr := first.UpdateHub(ctx, hub.ID, HubWrite{BaseURL: replacementURL, APIToken: "token-b"})
		firstDone <- updateErr
	}()
	waitForHubWriterLock(t, database.Pool, firstName)
	go func() {
		_, updateErr := second.UpdateHub(ctx, hub.ID, HubWrite{BaseURL: originalURL})
		secondDone <- updateErr
	}()
	waitForHubWriterLock(t, database.Pool, secondName)

	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := waitHubUpdate(t, firstDone); err != nil {
		t.Fatalf("first credential replacement failed: %v", err)
	}
	if err := waitHubUpdate(t, secondDone); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("stale partial update retained a token bound to another target: %v", err)
	}

	persisted, token, err := database.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.BaseURL != replacementURL || token != "token-b" {
		t.Fatalf("credential binding was split: base_url=%q token=%q", persisted.BaseURL, token)
	}
}

func TestGetHubCredentialUsesOneConsistentSnapshotIntegration(t *testing.T) {
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

	marker := fmt.Sprintf("hub-snapshot-%d", time.Now().UnixNano())
	oldURL := "https://" + marker + "-old.internal"
	newURL := "https://" + marker + "-new.internal"
	hub, err := database.CreateHub(ctx, HubWrite{
		Name: marker, Network: "test", BaseURL: oldURL, APIToken: "token-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.DeleteHub(context.Background(), hub.ID) }()

	newEncrypted, err := cipher.Encrypt([]byte("token-new"), fmt.Sprintf("hub:%d:token", hub.ID))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `UPDATE hubs SET base_url=$2,api_token_encrypted=$3 WHERE id=$1`, hub.ID, newURL, newEncrypted); err != nil {
		t.Fatal(err)
	}

	tracer := &hubQueryCounter{}
	reader := openHubCredentialTestStore(t, dsn, cipher, marker+"-snapshot-reader", tracer)
	tracer.count.Store(0)
	before, beforeToken, err := reader.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queries := tracer.count.Load(); queries != 1 {
		t.Fatalf("Hub configuration and token were read with %d SQL statements, want 1", queries)
	}
	if before.BaseURL != oldURL || beforeToken != "token-old" {
		t.Fatalf("reader observed an uncommitted or split credential: base_url=%q token=%q", before.BaseURL, beforeToken)
	}

	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tracer.count.Store(0)
	after, afterToken, err := reader.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queries := tracer.count.Load(); queries != 1 {
		t.Fatalf("Hub configuration and token were read with %d SQL statements after commit, want 1", queries)
	}
	if after.BaseURL != newURL || afterToken != "token-new" {
		t.Fatalf("reader observed a split committed credential: base_url=%q token=%q", after.BaseURL, afterToken)
	}
}

func TestStaleHubResponseCannotPopulateReplacementTargetIntegration(t *testing.T) {
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

	marker := fmt.Sprintf("hub-stale-response-%d", time.Now().UnixNano())
	hub, err := database.CreateHub(ctx, HubWrite{
		Name: marker, Network: "test", BaseURL: "https://" + marker + "-a.internal", APIToken: "token-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.DeleteHub(context.Background(), hub.ID) }()

	// This is the exact snapshot that an in-flight request to endpoint A keeps
	// after its remote response has arrived.
	requestHub, token, err := database.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-a" || requestHub.CredentialGeneration == "" {
		t.Fatalf("initial credential was not captured atomically: token=%q generation=%q", token, requestHub.CredentialGeneration)
	}
	existingStored, err := database.SyncHubUsersIfCurrent(ctx, requestHub, []integration.JupyterUser{{
		Name: marker + "-existing-endpoint-a",
		Raw:  []byte(`{}`),
	}})
	if err != nil || !existingStored {
		t.Fatalf("failed to establish endpoint A state: stored=%v err=%v", existingStored, err)
	}
	healthStored, err := database.UpdateHubHealthIfCurrent(ctx, requestHub, true, "A-existing", "", map[string]any{"endpoint": "A-existing"})
	if err != nil || !healthStored {
		t.Fatalf("failed to establish endpoint A health: stored=%v err=%v", healthStored, err)
	}

	endpointB := "https://" + marker + "-b.internal"
	if _, err := database.UpdateHub(ctx, hub.ID, HubWrite{BaseURL: endpointB, APIToken: "token-b"}); err != nil {
		t.Fatal(err)
	}

	stored, err := database.SyncHubUsersIfCurrent(ctx, requestHub, []integration.JupyterUser{{
		Name: marker + "-from-endpoint-a",
		Raw:  []byte(`{"name":"endpoint-a"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if stored {
		t.Fatal("stale endpoint A user response was accepted after replacement with endpoint B")
	}
	healthStored, err = database.UpdateHubHealthIfCurrent(ctx, requestHub, true, "A-5.3", "", map[string]any{"endpoint": "A"})
	if err != nil {
		t.Fatal(err)
	}
	if healthStored {
		t.Fatal("stale endpoint A health response was accepted after replacement with endpoint B")
	}

	var users int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_users WHERE hub_id=$1`, hub.ID).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatalf("endpoint A users leaked under endpoint B Hub ID: %d", users)
	}
	persisted, persistedToken, err := database.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.BaseURL != endpointB || persistedToken != "token-b" || persisted.Status != "unknown" || persisted.Version != "" || persisted.LastSeenAt != nil || strings.Contains(string(persisted.Snapshot), `"A"`) {
		t.Fatalf("endpoint A state leaked into endpoint B: hub=%#v token=%q", persisted, persistedToken)
	}

	// A response obtained from the replacement generation still persists.
	currentHub, _, err := database.GetHubCredential(ctx, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	endpointBUser := marker + "-from-endpoint-b"
	stored, err = database.SyncHubUsersIfCurrent(ctx, currentHub, []integration.JupyterUser{{
		Name:    endpointBUser,
		Raw:     []byte(`{}`),
		Servers: map[string]json.RawMessage{"lab": json.RawMessage(`{"ready":true,"url":"/user/b/lab"}`)},
	}})
	if err != nil || !stored {
		t.Fatalf("current endpoint B response was not stored: stored=%v err=%v", stored, err)
	}
	for _, version := range []string{"B-health-1", "B-health-2"} {
		healthStored, err = database.UpdateHubHealthIfCurrent(ctx, currentHub, true, version, "", map[string]any{"endpoint": "B"})
		if err != nil || !healthStored {
			t.Fatalf("non-configuration health update changed the credential generation: version=%q stored=%v err=%v", version, healthStored, err)
		}
	}

	var serverID int64
	if err := database.Pool.QueryRow(ctx, `SELECT id FROM servers WHERE hub_id=$1 AND username=$2`, hub.ID, endpointBUser).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	tracer := &hubQueryCounter{}
	actionReader := openHubCredentialTestStore(t, dsn, cipher, marker+"-action-reader", tracer)
	tracer.count.Store(0)
	actionServer, actionHub, actionToken, err := actionReader.GetServerActionCredential(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if queries := tracer.count.Load(); queries != 1 {
		t.Fatalf("server identity and Hub credential used %d SQL statements, want 1", queries)
	}
	if actionServer.Username != endpointBUser || actionHub.BaseURL != endpointB || actionToken != "token-b" {
		t.Fatalf("server action target was mixed: server=%#v hub=%#v token=%q", actionServer, actionHub, actionToken)
	}

	endpointC := "https://" + marker + "-c.internal"
	if _, err := database.UpdateHub(ctx, hub.ID, HubWrite{BaseURL: endpointC, APIToken: "token-c"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.GetServerActionCredential(ctx, serverID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("endpoint B server remained actionable after replacement with endpoint C: %v", err)
	}
}

type hubQueryCounter struct {
	count atomic.Int32
}

func (c *hubQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.count.Add(1)
	return ctx
}

func (*hubQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func openHubCredentialTestStore(t *testing.T, dsn string, cipher *secure.Cipher, applicationName string, tracer pgx.QueryTracer) *Store {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.HealthCheckPeriod = time.Hour
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Store{Pool: pool, Cipher: cipher}
}

func waitForHubWriterLock(t *testing.T, pool *pgxpool.Pool, applicationName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database()
				  AND application_name=$1
				  AND state='active'
				  AND wait_event_type='Lock'
			)`, applicationName).Scan(&waiting)
		if err != nil {
			t.Fatalf("inspect writer lock state: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("writer %q did not block on the Hub row: %v", applicationName, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitHubUpdate(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Hub update")
		return nil
	}
}

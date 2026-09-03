package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/migrations"
	"github.com/jackc/pgx/v5"
)

func TestLegacyScalarSettingsUpgradeIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	base, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close(ctx)

	schema := fmt.Sprintf("jupiq_upgrade_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(ctx, `CREATE SCHEMA `+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(ctx, `DROP SCHEMA IF EXISTS `+quotedSchema+` CASCADE`) }()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	scopedDSN := parsed.String()
	legacy, err := pgx.Connect(ctx, scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_initial.sql", "002_llm_usage.sql"} {
		script, err := migrations.FS.ReadFile(name)
		if err != nil {
			legacy.Close(ctx)
			t.Fatal(err)
		}
		if _, err := legacy.Exec(ctx, string(script)); err != nil {
			legacy.Close(ctx)
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
	}
	if _, err := legacy.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES('001_initial.sql'),('002_llm_usage.sql')`); err != nil {
		legacy.Close(ctx)
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, `INSERT INTO settings(setting_key,value) VALUES
		('ai','"broken"'),('system','42'),('prometheus','false'),('notifications','[]'),('security','null'),
		('workflow','"broken"'),('auth.oidc','42'),('kubernetes','false'),('features','[]'),('llm_usage','null')`); err != nil {
		legacy.Close(ctx)
		t.Fatal(err)
	}
	var legacyUserID, legacyRoleID int64
	if err := legacy.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, "legacy-scope-"+schema).Scan(&legacyUserID); err != nil {
		legacy.Close(ctx)
		t.Fatal(err)
	}
	if err := legacy.QueryRow(ctx, `INSERT INTO roles(role_key,name,permissions) VALUES($1,$1,'["hubs:read"]') RETURNING id`, "legacy-scope-"+schema).Scan(&legacyRoleID); err != nil {
		legacy.Close(ctx)
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES($1,$2)`, legacyUserID, legacyRoleID); err != nil {
		legacy.Close(ctx)
		t.Fatal(err)
	}
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}

	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, scopedDSN, cipher)
	if err != nil {
		t.Fatalf("upgrade v1.0 scalar settings: %v", err)
	}
	defer database.Close()
	var scopeMode string
	if err := database.Pool.QueryRow(ctx, `SELECT scope_mode FROM user_roles WHERE user_id=$1 AND role_id=$2`, legacyUserID, legacyRoleID).Scan(&scopeMode); err != nil {
		t.Fatal(err)
	}
	if scopeMode != "global" {
		t.Fatalf("legacy role assignment did not remain global: %q", scopeMode)
	}
	legacyUser, err := database.GetUser(ctx, legacyUserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyUser.GlobalPermissions) != 1 || legacyUser.GlobalPermissions[0] != "hubs:read" || len(legacyUser.RoleBindings) != 1 || legacyUser.RoleBindings[0].ScopeMode != "global" {
		t.Fatalf("legacy access was not hydrated as a global binding: %#v", legacyUser)
	}
	for _, key := range []string{"ai", "system", "prometheus", "notifications", "security", "workflow", "auth.oidc", "kubernetes", "features", "llm_usage"} {
		var raw json.RawMessage
		if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key=$1`, key).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			t.Fatalf("%s was not repaired to an object: %s (%v)", key, raw, err)
		}
	}
	var ai map[string]any
	if err := database.GetSetting(ctx, "ai", &ai); err != nil {
		t.Fatal(err)
	}
	if ai["verify_tls"] != true || ai["streaming"] != true || ai["enabled"] != false {
		t.Fatalf("AI security defaults were not repaired: %#v", ai)
	}
}

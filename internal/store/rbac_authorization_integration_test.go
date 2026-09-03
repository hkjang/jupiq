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
)

func TestAuthorizedRBACMutationsRecheckCeilingAfterLockIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("rbac-cas-%d", time.Now().UnixNano())

	actorRole, err := database.SaveRole(ctx, Role{Key: marker + "-actor", Name: "제한 역할 관리자", Permissions: []string{"roles:write", "dashboard:read"}})
	if err != nil {
		t.Fatal(err)
	}
	targetRole, err := database.SaveRole(ctx, Role{Key: marker + "-target", Name: "일반 역할", Permissions: []string{"dashboard:read"}})
	if err != nil {
		t.Fatal(err)
	}
	var actorID, targetUserID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-actor").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-target").Scan(&targetUserID); err != nil {
		t.Fatal(err)
	}
	if err := database.SetUserRoles(ctx, actorID, []int64{actorRole.ID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{actorID, targetUserID})
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM roles WHERE id=ANY($1::bigint[])`, []int64{actorRole.ID, targetRole.ID})
	})

	// The holder represents another administrator changing the target role after
	// the caller's HTTP preflight but before the authorized Store command obtains
	// the RBAC lock.
	holder, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacMutationLockKey); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- database.SetUserRolesAuthorized(ctx, targetUserID, []int64{targetRole.ID}, actorID)
	}()
	waitForSettingsLockWaiter(t, database)
	wildcard, _ := json.Marshal([]string{"*"})
	if _, err := holder.Exec(ctx, `UPDATE roles SET permissions=$2,updated_at=now() WHERE id=$1`, targetRole.ID, wildcard); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, ErrRoleGrantCeiling) {
			t.Fatalf("stale role authorization was not rejected: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authorized role assignment did not finish after lock release")
	}
	var assigned bool
	if err := database.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role_id=$2)`, targetUserID, targetRole.ID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned {
		t.Fatal("role upgraded beyond the actor ceiling was assigned")
	}

	// Save and delete commands also inspect the current role under the lock,
	// rather than trusting the weaker role observed by the HTTP preflight.
	targetRole.Permissions = []string{"dashboard:read"}
	if _, err := database.SaveRoleAuthorized(ctx, targetRole, actorID); !errors.Is(err, ErrRoleGrantCeiling) {
		t.Fatalf("authorized save accepted a currently stronger role: %v", err)
	}
	if err := database.DeleteRoleAuthorized(ctx, targetRole.ID, actorID); !errors.Is(err, ErrRoleGrantCeiling) {
		t.Fatalf("authorized delete accepted a currently stronger role: %v", err)
	}
}

func TestHubScopeBindingAndDeleteAreSerializedIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("scope-delete-cas-%d", time.Now().UnixNano())
	role, err := database.SaveRole(ctx, Role{Key: marker + "-role", Name: "Hub 범위", Permissions: []string{"hubs:read"}})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := database.CreateHub(ctx, HubWrite{Name: marker + "-hub", BaseURL: "https://scope-delete.internal", Network: marker, APIToken: "token", CollectIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-user").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM roles WHERE id=$1`, role.ID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM hubs WHERE id=$1`, hub.ID)
	})

	holder, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		t.Fatal(err)
	}
	bindingResult := make(chan error, 1)
	go func() {
		bindingResult <- database.SetUserRoleBindings(ctx, userID, []RoleBinding{{RoleID: role.ID, ScopeMode: "restricted", Scopes: []ScopeClause{{Type: "hub", Value: fmt.Sprint(hub.ID)}}}})
	}()
	waitForSettingsLockWaiter(t, database)

	deleteResult := make(chan error, 1)
	go func() { deleteResult <- database.DeleteHub(ctx, hub.ID) }()
	// DeleteHub must queue behind the binding transaction's RBAC lock; it may not
	// delete/clean the Hub and then allow a stale scope clause to be inserted.
	select {
	case err := <-deleteResult:
		t.Fatalf("Hub delete bypassed the RBAC/inventory lock order: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-bindingResult:
		if err != nil {
			t.Fatalf("binding transaction failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("binding transaction did not finish")
	}
	select {
	case err := <-deleteResult:
		if err != nil {
			t.Fatalf("serialized Hub delete failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Hub delete did not finish")
	}
	var staleClauses int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM user_role_scope_clauses WHERE scope_type='hub' AND scope_value=$1`, fmt.Sprint(hub.ID)).Scan(&staleClauses); err != nil {
		t.Fatal(err)
	}
	if staleClauses != 0 {
		t.Fatalf("deleted Hub retained %d stale authorization clauses", staleClauses)
	}
}

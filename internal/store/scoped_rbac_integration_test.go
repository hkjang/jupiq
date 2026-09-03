package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

func TestScopedRoleBindingsFilterBeforePaginationIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("scope-%d", time.Now().UnixNano())

	var hubOne, hubTwo int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1) RETURNING id`, marker+"-one", "https://"+marker+"-one.internal").Scan(&hubOne); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1) RETURNING id`, marker+"-two", "https://"+marker+"-two.internal").Scan(&hubTwo); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM roles WHERE role_key IN ($1,$2)`, marker+"-hub-role", marker+"-data-role")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=ANY($1::bigint[])`, []int64{hubOne, hubTwo})
	}()

	insertManaged := func(hubID int64, username, department string) int64 {
		var id int64
		if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name,department) VALUES($1,$2,$2,$3) RETURNING id`, hubID, username, department).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status) VALUES($1,$2,$3,'','running')`, hubID, id, username); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertManaged(hubOne, marker+"-allowed", "")
	if _, err := database.Pool.Exec(ctx, `INSERT INTO users(username,display_name,department,auth_source) VALUES($1,$1,'AI','oidc')`, marker+"-allowed"); err != nil {
		t.Fatal(err)
	}
	insertManaged(hubOne, marker+"-wrong-department", "재무")
	insertManaged(hubTwo, marker+"-wrong-hub", "AI")

	var operatorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-operator").Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	hubRole, err := database.SaveRole(ctx, Role{Key: marker + "-hub-role", Name: "범위 Hub", Permissions: []string{"hubs:read", "hubs:write"}})
	if err != nil {
		t.Fatal(err)
	}
	dataRole, err := database.SaveRole(ctx, Role{Key: marker + "-data-role", Name: "범위 데이터", Permissions: []string{"users:read", "servers:read", "servers:operate"}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []RoleBinding{
		{RoleID: hubRole.ID, ScopeMode: "restricted", Scopes: []ScopeClause{{Type: "hub", Value: fmt.Sprint(hubOne)}}},
		{RoleID: dataRole.ID, ScopeMode: "restricted", Scopes: []ScopeClause{{Type: "hub", Value: fmt.Sprint(hubOne)}, {Type: "department", Value: "AI"}}},
	}
	if err := database.SetUserRoleBindings(ctx, operatorID, bindings); err != nil {
		t.Fatal(err)
	}
	operator, err := database.GetUser(ctx, operatorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operator.GlobalPermissions) != 0 || len(operator.RoleBindings) != 2 || len(operator.PermissionGrants) != 2 {
		t.Fatalf("role bindings were not hydrated safely: %#v", operator)
	}
	if err := database.SetUserRoles(ctx, operatorID, []int64{hubRole.ID}); !errors.Is(err, ErrScopeRequired) {
		t.Fatalf("legacy role_ids request promoted/replaced a restricted binding: %v", err)
	}

	hubs, err := database.ListHubsWithAccess(ctx, AccessFilter{Groups: []ScopeGroup{{HubIDs: []int64{hubOne}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 1 || hubs[0].ID != hubOne {
		t.Fatalf("Hub scope leaked another Hub: %#v", hubs)
	}
	access := AccessFilter{Groups: []ScopeGroup{{HubIDs: []int64{hubOne}, Departments: []string{"AI"}}}}
	users, page, err := database.ListManagedUsersWithAccess(ctx, 1, 1, 0, marker, access)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(users) != 1 || users[0].Username != marker+"-allowed" {
		t.Fatalf("managed-user scope was applied after pagination or leaked a row: page=%#v users=%#v", page, users)
	}
	servers, serverPage, err := database.ListServersWithAccess(ctx, 1, 1, 0, "", marker, access)
	if err != nil {
		t.Fatal(err)
	}
	if serverPage.Total != 1 || len(servers) != 1 || servers[0].Username != marker+"-allowed" || servers[0].Department != "AI" {
		t.Fatalf("server scope was applied after pagination or leaked a row: page=%#v servers=%#v", serverPage, servers)
	}
	users, page, err = database.ListManagedUsersWithAccess(ctx, 1, 20, hubTwo, marker, access)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(users) != 0 {
		t.Fatalf("explicit Hub filter bypassed assigned scope: page=%#v users=%#v", page, users)
	}
}

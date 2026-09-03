package auth

import (
	"testing"

	"github.com/hkjang/jupiq/internal/store"
)

func TestPrincipalAPIKeyScopesCannotElevateRole(t *testing.T) {
	empty := Principal{UserPermissions: []string{"*"}, APIKeyID: 1}
	if empty.Allows("dashboard:read") {
		t.Fatal("empty API key scopes must deny all permissioned operations")
	}
	p := Principal{UserPermissions: []string{"*"}, APIKeyID: 1, APIKeyScopes: []string{"mcp:use"}}
	if p.Allows("dashboard:read") {
		t.Fatal("MCP-only API key must not gain dashboard permission")
	}
	if !p.Allows("mcp:use") {
		t.Fatal("declared API key permission should be allowed")
	}
	p = Principal{UserPermissions: []string{"dashboard:read"}, APIKeyID: 1, APIKeyScopes: []string{"*"}}
	if p.Allows("hubs:read") {
		t.Fatal("API key wildcard must not exceed the user's role")
	}
}

func TestPrincipalScopedPermissionAndAPIKeyIntersection(t *testing.T) {
	p := Principal{User: store.User{
		GlobalPermissions: []string{},
		PermissionGrants: []store.PermissionGrant{{
			Permissions: []string{"users:read", "servers:*"},
			HubIDs:      []int64{7, 9},
			Departments: []string{"AI 연구"},
		}},
	}, UserPermissions: []string{"users:read", "servers:*"}}
	if p.Allows("users:read") {
		t.Fatal("restricted permission became global")
	}
	if !p.AllowsTarget("users:read", 7, "ai 연구") {
		t.Fatal("matching Hub and department should be allowed")
	}
	if p.AllowsTarget("users:read", 7, "재무") || p.AllowsTarget("users:read", 8, "AI 연구") {
		t.Fatal("Hub and department dimensions must both match")
	}
	if !p.AllowsTarget("servers:operate", 9, "AI 연구") {
		t.Fatal("namespace wildcard should apply inside its assigned scope")
	}

	p.APIKeyID = 10
	p.APIKeyScopes = []string{"users:read"}
	if !p.AllowsTarget("users:read", 7, "AI 연구") {
		t.Fatal("API key permission should intersect with a matching user scope")
	}
	if p.AllowsTarget("servers:operate", 7, "AI 연구") {
		t.Fatal("API key must not gain a scoped permission it did not request")
	}
	if access := p.AccessFilter("servers:operate"); access.Global || len(access.Groups) != 0 {
		t.Fatalf("denied API key must receive a deny-all SQL filter: %#v", access)
	}
}

func TestPrincipalGlobalPermissionRemainsSubjectToAPIKey(t *testing.T) {
	p := Principal{User: store.User{GlobalPermissions: []string{"hubs:*"}}, APIKeyID: 11, APIKeyScopes: []string{"hubs:read"}}
	if !p.Allows("hubs:read") || !p.AccessFilter("hubs:read").Global {
		t.Fatal("matching global user and API key permissions should be global")
	}
	if p.Allows("hubs:write") || p.AllowsTarget("hubs:write", 1, "") {
		t.Fatal("API key scope must constrain a global user permission")
	}
}

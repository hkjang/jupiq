package auth

import "testing"

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

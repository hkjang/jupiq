package store

import "testing"

func TestScopesMustBeNonEmptySubset(t *testing.T) {
	if ValidateScopes(nil) == nil {
		t.Fatal("empty scopes accepted")
	}
	if ScopesWithinPermissions(nil, []string{"*"}) {
		t.Fatal("empty scopes accepted as subset")
	}
	if !ScopesWithinPermissions([]string{"dashboard:read"}, []string{"dashboard:*"}) {
		t.Fatal("valid subset rejected")
	}
	if ScopesWithinPermissions([]string{"hubs:write"}, []string{"hubs:read"}) {
		t.Fatal("scope escalation accepted")
	}
}

package store

import (
	"reflect"
	"testing"
)

func TestAccessPredicatePreservesGrantBoundaries(t *testing.T) {
	filter := AccessFilter{Groups: []ScopeGroup{
		{HubIDs: []int64{1, 2}, Departments: []string{"AI"}},
		{HubIDs: []int64{3}},
	}}
	predicate, args := AccessPredicate(filter, "u.hub_id", "u.department", 4)
	want := "((u.hub_id=ANY($4::bigint[]) AND lower(btrim(COALESCE(u.department,'')))=ANY($5::text[])) OR (u.hub_id=ANY($6::bigint[])))"
	if predicate != want {
		t.Fatalf("unexpected predicate\nwant: %s\n got: %s", want, predicate)
	}
	if len(args) != 3 || !reflect.DeepEqual(args[0], []int64{1, 2}) || !reflect.DeepEqual(args[1], []string{"ai"}) || !reflect.DeepEqual(args[2], []int64{3}) {
		t.Fatalf("unexpected predicate arguments: %#v", args)
	}
	if predicate, args := AccessPredicate(AccessFilter{}, "u.hub_id", "u.department", 1); predicate != "FALSE" || len(args) != 0 {
		t.Fatalf("empty access must fail closed: predicate=%q args=%#v", predicate, args)
	}
	if predicate, args := AccessPredicate(AccessFilter{Global: true}, "", "", 1); predicate != "TRUE" || len(args) != 0 {
		t.Fatalf("global access should not add SQL arguments: predicate=%q args=%#v", predicate, args)
	}
}

func TestAccessPredicateCannotDropAnUnrepresentableDimension(t *testing.T) {
	filter := AccessFilter{Groups: []ScopeGroup{{HubIDs: []int64{1}, Departments: []string{"AI"}}}}
	predicate, args := AccessPredicate(filter, "id", "", 1)
	if predicate != "FALSE" || len(args) != 0 {
		t.Fatalf("Hub-only query must not drop a department restriction: predicate=%q args=%#v", predicate, args)
	}
}

func TestNormalizeRoleBindings(t *testing.T) {
	bindings, roleIDs, err := normalizeRoleBindings([]RoleBinding{{
		RoleID: 7, ScopeMode: "restricted", Scopes: []ScopeClause{{Type: "hub", Value: " 12 "}, {Type: "department", Value: " AI "}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roleIDs, []int64{7}) || bindings[0].Scopes[0].Value != "12" || bindings[0].Scopes[1].Value != "AI" {
		t.Fatalf("unexpected normalized binding: %#v / %#v", bindings, roleIDs)
	}
	invalid := []RoleBinding{{RoleID: 7, ScopeMode: "restricted"}}
	if _, _, err := normalizeRoleBindings(invalid); err == nil {
		t.Fatal("restricted binding without a clause must be rejected")
	}
	invalid = []RoleBinding{{RoleID: 7, ScopeMode: "global", Scopes: []ScopeClause{{Type: "hub", Value: "1"}}}}
	if _, _, err := normalizeRoleBindings(invalid); err == nil {
		t.Fatal("global binding with clauses must be rejected")
	}
}

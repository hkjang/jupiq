package store

import "testing"

func TestOrderByUsesOnlyAllowlistedColumns(t *testing.T) {
	columns := map[string]string{"name": "name", "updated_at": "updated_at"}
	if got := orderBy(columns, Sort{Key: "name"}, "id", "id"); got != "name ASC NULLS LAST,id" {
		t.Fatalf("ascending clause = %q", got)
	}
	if got := orderBy(columns, Sort{Key: "updated_at", Direction: "DESC"}, "id", "id"); got != "updated_at DESC NULLS LAST,id" {
		t.Fatalf("descending clause = %q", got)
	}
}

func TestOrderByRejectsUnknownAndInjectedKeys(t *testing.T) {
	columns := map[string]string{"name": "name"}
	for _, key := range []string{
		"", "unknown", "name; DROP TABLE resources",
		"name)--", "1", "(SELECT 1)", "name DESC",
	} {
		if got := orderBy(columns, Sort{Key: key}, "updated_at DESC", "id"); got != "updated_at DESC" {
			t.Fatalf("orderBy(%q) = %q, want the fallback", key, got)
		}
	}
}

func TestOrderByIgnoresInjectedDirection(t *testing.T) {
	columns := map[string]string{"name": "name"}
	got := orderBy(columns, Sort{Key: "name", Direction: "desc; DROP TABLE resources"}, "id", "id")
	if got != "name ASC NULLS LAST,id" {
		t.Fatalf("clause = %q, want ascending (only exact 'desc' descends)", got)
	}
}

package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

// TestListSearchTreatsLikeMetacharactersAsLiteralsIntegration proves that every
// list filter reads the same search string the same way the global search does:
// %, _ and \ are literal characters, and a blank filter still lists everything.
func TestListSearchTreatsLikeMetacharactersAsLiteralsIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("list-search-%d", time.Now().UnixNano())
	if err := database.Seed(ctx, marker, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	underscore := marker + "-hong_gildong"
	literal := marker + "-hong1gildong"
	backslash := marker + `-back\slash`
	plain := marker + "-backslash"
	names := []string{underscore, literal, backslash, plain}

	var hubID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,'https://hub.invalid',$1) RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = database.Pool.Exec(cleanup, `DELETE FROM servers WHERE hub_id=$1`, hubID)
		_, _ = database.Pool.Exec(cleanup, `DELETE FROM managed_users WHERE hub_id=$1`, hubID)
		_, _ = database.Pool.Exec(cleanup, `DELETE FROM hubs WHERE id=$1`, hubID)
		_, _ = database.Pool.Exec(cleanup, `DELETE FROM resources WHERE kind=$1`, marker)
		_, _ = database.Pool.Exec(cleanup, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
	})
	for _, name := range names {
		if _, err := database.Pool.Exec(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc')`, name); err != nil {
			t.Fatal(err)
		}
		var managedUserID int64
		if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name) VALUES($1,$2,$2) RETURNING id`, hubID, name).Scan(&managedUserID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status) VALUES($1,$2,$3,'','running')`, hubID, managedUserID, name); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `INSERT INTO resources(kind,name) VALUES($1,$2)`, marker, name); err != nil {
			t.Fatal(err)
		}
	}

	type lister struct {
		name string
		list func(search string) ([]string, Page, error)
	}
	listers := []lister{
		{"ListLocalUsers", func(search string) ([]string, Page, error) {
			items, page, err := database.ListLocalUsers(ctx, 1, 100, search)
			found := []string{}
			for _, item := range items {
				if strings.HasPrefix(item.Username, marker+"-") {
					found = append(found, item.Username)
				}
			}
			return found, page, err
		}},
		{"ListManagedUsersWithAccess", func(search string) ([]string, Page, error) {
			items, page, err := database.ListManagedUsersWithAccess(ctx, 1, 100, hubID, search, AccessFilter{Global: true}, Sort{})
			found := []string{}
			for _, item := range items {
				found = append(found, item.Username)
			}
			return found, page, err
		}},
		{"ListServersWithAccess", func(search string) ([]string, Page, error) {
			items, page, err := database.ListServersWithAccess(ctx, 1, 100, hubID, "", search, AccessFilter{Global: true}, Sort{})
			found := []string{}
			for _, item := range items {
				found = append(found, item.Username)
			}
			return found, page, err
		}},
		{"ListResources", func(search string) ([]string, Page, error) {
			items, page, err := database.ListResources(ctx, marker, 1, 100, "", search, nil, Sort{})
			found := []string{}
			for _, item := range items {
				found = append(found, item.Name)
			}
			return found, page, err
		}},
	}

	for _, l := range listers {
		t.Run(l.name, func(t *testing.T) {
			// An underscore in the query must not match an arbitrary character.
			found, page, err := l.list(underscore)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 1 || found[0] != underscore {
				t.Fatalf("search %q returned %v, want only %q", underscore, found, underscore)
			}
			if page.Total != 1 {
				t.Fatalf("search %q reported Total=%d, want 1 (matching the %d rows returned)", underscore, page.Total, len(found))
			}

			// A percent sign in the query must not match anything either.
			wildcard := marker + "-hong%gildong"
			found, page, err = l.list(wildcard)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 0 || page.Total != 0 {
				t.Fatalf("search %q returned %v (Total=%d), want no rows", wildcard, found, page.Total)
			}

			// A backslash must select the row that really contains one.
			found, page, err = l.list(backslash)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 1 || found[0] != backslash || page.Total != 1 {
				t.Fatalf("search %q returned %v (Total=%d), want only %q", backslash, found, page.Total, backslash)
			}

			// A blank or whitespace-only filter still lists everything.
			blank, blankPage, err := l.list("")
			if err != nil {
				t.Fatal(err)
			}
			spaces, spacesPage, err := l.list("   ")
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(blank)
			sort.Strings(spaces)
			if len(blank) != len(names) {
				t.Fatalf("blank search returned %v, want all %d seeded rows", blank, len(names))
			}
			if fmt.Sprint(blank) != fmt.Sprint(spaces) || blankPage.Total != spacesPage.Total {
				t.Fatalf("whitespace-only search returned %v (Total=%d), want the blank-search result %v (Total=%d)", spaces, spacesPage.Total, blank, blankPage.Total)
			}
		})
	}
}

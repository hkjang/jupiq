package store

import (
	"net/url"
	"testing"
)

func TestSearchPatternEscapesLikeMetacharacters(t *testing.T) {
	if got, want := searchPattern(` 50%_off\today `), `%50\%\_off\\today%`; got != want {
		t.Fatalf("searchPattern()=%q want %q", got, want)
	}
}

func TestSearchResultPathsEncodeUntrustedNames(t *testing.T) {
	if got, want := "/users/"+url.PathEscape("team/user #1"), "/users/team%2Fuser%20%231"; got != want {
		t.Fatalf("user path=%q want %q", got, want)
	}

	got := searchFilterPath("/hubs", "업무망 & 분석#1")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/hubs" || parsed.Query().Get("search") != "업무망 & 분석#1" {
		t.Fatalf("unsafe or lossy query path: %q", got)
	}
}

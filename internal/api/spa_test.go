package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDist(t *testing.T, files map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for name, body := range files {
		path := filepath.Join("web/dist", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func serveSPAPath(t *testing.T, urlPath string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.URL.Path = urlPath
	rec := httptest.NewRecorder()
	s.serveSPA(rec, r)
	return rec
}

func TestServeSPACachesHashedAssetsImmutably(t *testing.T) {
	writeDist(t, map[string]string{
		"index.html":               "<!doctype html>",
		"assets/index-D1a2B3c4.js": "console.log(1)",
		"favicon.svg":              "<svg/>",
	})

	cases := []struct {
		path string
		want string
		body string
	}{
		{"/assets/index-D1a2B3c4.js", "public, max-age=31536000, immutable", "console.log(1)"},
		{"/favicon.svg", "public, max-age=0, must-revalidate", "<svg/>"},
		{"/dashboard", "no-store", "<!doctype html>"},
	}
	for _, tc := range cases {
		rec := serveSPAPath(t, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d want 200", tc.path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Fatalf("%s: Cache-Control=%q want %q", tc.path, got, tc.want)
		}
		if rec.Body.String() != tc.body {
			t.Fatalf("%s: body=%q want %q", tc.path, rec.Body.String(), tc.body)
		}
	}
}

func TestServeSPARejectsAPIPathsWithJSONError(t *testing.T) {
	writeDist(t, map[string]string{"index.html": "<!doctype html>"})
	for _, path := range []string{"/api/v1/unknown", "/mcp/anything"} {
		rec := serveSPAPath(t, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Fatalf("%s: body=%q want JSON API error", path, rec.Body.String())
		}
	}
}

func TestServeSPADoesNotEscapeDistRoot(t *testing.T) {
	writeDist(t, map[string]string{"index.html": "<!doctype html>"})
	if err := os.WriteFile("secret.txt", []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	for _, path := range []string{"/../secret.txt", "/../../secret.txt", "/assets/../../secret.txt"} {
		rec := serveSPAPath(t, path)
		if strings.Contains(rec.Body.String(), "top secret") {
			t.Fatalf("%s: served a file outside web/dist", path)
		}
	}
}

func TestServeSPAWithoutBuildOutputReturns404(t *testing.T) {
	writeDist(t, nil)
	rec := serveSPAPath(t, "/dashboard")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 when web/dist is missing", rec.Code)
	}
}

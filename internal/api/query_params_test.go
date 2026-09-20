package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/auth"
)

// 목록·지표 핸들러는 정수가 아니거나 음수인 page·page_size·limit·hub_id를 store에
// 닿기 전에 400 invalid_query로 거부한다. Server의 Store가 nil이므로 파싱을
// 통과해 store를 부르면 nil 역참조 panic으로 드러난다 — 가짜 store를 두지 않는다.
func TestListHandlersRejectMalformedIntegerQueriesBeforeStore(t *testing.T) {
	s := &Server{}
	handlers := map[string]http.HandlerFunc{
		"auditList":        s.auditList,
		"servers":          s.servers,
		"managedUsers":     s.managedUsers,
		"localUsers":       s.localUsers,
		"metrics":          s.metrics,
		"usageConsumption": s.usageConsumption,
		"resourceList":     func(w http.ResponseWriter, r *http.Request) { s.resourceList(w, r, "policy") },
	}
	queries := map[string][]string{
		"auditList":        {"page=abc", "page_size=abc", "page=-1", "page_size=-5"},
		"servers":          {"page=abc", "page_size=-1", "hub_id=abc", "hub_id=-1"},
		"managedUsers":     {"page=abc", "page_size=-1", "hub_id=abc", "hub_id=-1"},
		"localUsers":       {"page=abc", "page_size=-1"},
		"metrics":          {"limit=abc", "limit=-1"},
		"usageConsumption": {"limit=abc", "limit=-1"},
		"resourceList":     {"page=abc", "page_size=-1"},
	}
	for name, handler := range handlers {
		for _, query := range queries[name] {
			t.Run(name+"?"+query, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/x?"+query, nil)
				req = withPrincipal(req, auth.Principal{UserPermissions: []string{"*"}})
				rec := httptest.NewRecorder()
				handler(rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
				}
				var body errorBody
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body %q: %v", rec.Body.String(), err)
				}
				param := query[:strings.Index(query, "=")]
				if body.Error.Code != "invalid_query" || body.Error.Message != param+"은(는) 0 이상의 정수여야 합니다" {
					t.Fatalf("unexpected error body: %s", rec.Body.String())
				}
			})
		}
	}
}

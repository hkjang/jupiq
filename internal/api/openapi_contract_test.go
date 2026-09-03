package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// openapi.yaml의 servers 항목이 선언한 접두사. 문서의 경로는 여기에 상대적이다.
const openapiServerPrefix = "/api/v1"

// undocumentedRoutes는 OpenAPI 문서에 없는 것이 정상인 등록 경로와 그 이유다.
// 새 경로를 추가하면서 문서를 빼먹으면 계약 테스트가 실패한다.
var undocumentedRoutes = map[string]string{
	"GET /":                         "SPA 정적 파일 응답",
	"GET /healthz":                  "서버 접두사 밖의 컨테이너 liveness probe",
	"GET /readyz":                   "서버 접두사 밖의 컨테이너 readiness probe",
	"POST /mcp":                     "POST /api/v1/mcp와 같은 핸들러를 쓰는 MCP 별칭",
	"GET /api/v1/openapi.yaml":      "문서 자신을 내려주는 경로",
	"GET /api/v1/live":              "GET /api/v1/dashboard/live의 레거시 별칭",
	"POST /api/v1/approvals":        "승인 요청은 대상 작업 API가 만들므로 항상 405",
	"PUT /api/v1/approvals/{id}":    "승인 상태는 review·approve·reject로만 바꾸므로 항상 405",
	"DELETE /api/v1/approvals/{id}": "감사 추적을 위해 삭제를 막으므로 항상 405",
}

type recordingRouter struct{ patterns []string }

func (r *recordingRouter) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
}

// registeredRoutes는 Handler와 같은 순서로 등록 함수를 호출해 "METHOD /경로" 집합을 만든다.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	s := &Server{}
	rec := &recordingRouter{}
	s.registerPublic(rec)
	s.registerAuth(rec)
	s.registerCore(rec)
	s.registerHubs(rec)
	s.registerResources(rec)
	s.registerAI(rec)
	s.registerMCP(rec)
	rec.HandleFunc("GET /", s.serveSPA)
	routes := map[string]bool{}
	for _, pattern := range rec.patterns {
		if routes[pattern] {
			t.Errorf("route %s is registered more than once", pattern)
		}
		routes[pattern] = true
	}
	return routes
}

// specOperations는 openapi.yaml의 paths 블록에서 "METHOD /api/v1/경로" 집합을 읽는다.
// 경로 키는 두 칸, 메서드 키는 네 칸 들여쓰기라는 문서 규칙만 사용한다.
func specOperations(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi", "openapi.yaml"))
	if err != nil {
		t.Fatalf("openapi.yaml을 읽을 수 없습니다: %v", err)
	}
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	operations := map[string]bool{}
	inPaths, path := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
			if inPaths {
				break
			}
			inPaths = strings.HasPrefix(line, "paths:")
			continue
		}
		if !inPaths {
			continue
		}
		key := strings.TrimSpace(line)
		if colon := strings.Index(key, ":"); colon >= 0 {
			key = key[:colon]
		}
		switch {
		case strings.HasPrefix(line, "  /") && !strings.HasPrefix(line, "   "):
			path = key
		case path != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && methods[key]:
			operations[strings.ToUpper(key)+" "+openapiServerPrefix+path] = true
		}
	}
	if len(operations) == 0 {
		t.Fatalf("openapi.yaml에서 paths 블록을 찾지 못했습니다")
	}
	return operations
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestOpenAPIDocumentCoversEveryRegisteredRoute(t *testing.T) {
	routes, spec := registeredRoutes(t), specOperations(t)
	for _, route := range sortedKeys(routes) {
		if spec[route] {
			continue
		}
		if _, excepted := undocumentedRoutes[route]; !excepted {
			t.Errorf("%s는 등록됐지만 openapi.yaml에 없습니다. 문서를 추가하거나 undocumentedRoutes에 이유를 적으세요", route)
		}
	}
}

func TestOpenAPIDocumentHasNoRouteWithoutHandler(t *testing.T) {
	routes, spec := registeredRoutes(t), specOperations(t)
	for _, operation := range sortedKeys(spec) {
		if !routes[operation] {
			t.Errorf("openapi.yaml이 %s를 설명하지만 등록된 경로가 없습니다", operation)
		}
	}
}

func TestUndocumentedRouteExceptionsStayCurrent(t *testing.T) {
	routes, spec := registeredRoutes(t), specOperations(t)
	exceptions := make([]string, 0, len(undocumentedRoutes))
	for route := range undocumentedRoutes {
		exceptions = append(exceptions, route)
	}
	sort.Strings(exceptions)
	for _, route := range exceptions {
		if !routes[route] {
			t.Errorf("undocumentedRoutes에 %s가 남아 있지만 더 이상 등록되지 않습니다", route)
		}
		if spec[route] {
			t.Errorf("%s는 이제 openapi.yaml에 있으므로 undocumentedRoutes에서 지우세요", route)
		}
	}
}

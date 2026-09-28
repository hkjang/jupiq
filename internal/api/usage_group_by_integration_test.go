package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

// /usage는 같은 group_by를 추세와 소비량 두 경로에 넘기지만, 시간별 롤업에는
// project 열이 없어 소비량만 조용히 user로 되돌아간다. 한 응답 안에서 추세는
// 프로젝트별, consumption은 사용자별인데도 응답이 그 사실을 말하지 않으면
// 호출자는 두 목록을 같은 축으로 읽는다. 실제 Store와 실제 핸들러로 그 한
// 응답을 받아 두 축이 다르다는 것과 응답이 그것을 밝히는지를 함께 본다.
func TestUsageReportsWhichGroupTheConsumptionWasAggregatedByIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	marker := fmt.Sprintf("usage-group-by-%d", time.Now().UnixNano())
	project := marker + "-project"
	bucket := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO metric_samples(source,metric_name,labels,value,sampled_at)
		VALUES('test','cpu_cores',jsonb_build_object('username',$1::text,'project',$2::text),1.5,$3)`,
		marker, project, bucket.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE labels->>'username'=$1`, marker)
	}()
	// 네 그룹 열을 모두 같은 표식으로 채워, 어떤 group_by로 집계하든 우리 행을
	// 이름으로 찾을 수 있게 한다(빈 열은 'unknown'으로 뭉뚱그려진다).
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO resource_usage_hourly(bucket,username,hub_name,network,department,cpu_core_seconds,memory_byte_seconds,runtime_seconds,sample_count)
		VALUES($1,$2,$2,$2,$2,3600,0,3600,1)`, bucket, marker); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE username=$1`, marker)
	}()

	server := &Server{Store: database, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	call := func(t *testing.T, query string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		server.usage(response, httptest.NewRequest("GET", "/api/v1/usage"+query, nil))
		if response.Code != 200 {
			t.Fatalf("GET /api/v1/usage%s status=%d body=%s", query, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	// data() 래퍼가 {"data":…}로 감싸는지 여부에 관계없이 본문에서 응답 객체를 꺼낸다.
	unwrap := func(payload map[string]any) map[string]any {
		if inner, ok := payload["data"].(map[string]any); ok {
			return inner
		}
		return payload
	}
	// 소비량은 우리가 넣은 행만 본다 — 같은 DB의 다른 사용자 행은 무시한다.
	consumptionForMarker := func(t *testing.T, body map[string]any) map[string]any {
		t.Helper()
		rows, _ := body["consumption"].([]any)
		if len(rows) == 0 {
			t.Fatalf("consumption이 비어 있다: %#v", body["consumption"])
		}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if row["group"] == marker {
				return row
			}
		}
		t.Fatalf("consumption에 %q 행이 없다: %#v", marker, rows)
		return nil
	}

	t.Run("project는 추세만 묶이고 소비량은 user로 되돌아간다", func(t *testing.T) {
		body := unwrap(call(t, "?group_by=project&granularity=hour"))
		effective, ok := body["consumption_group_by"].(string)
		if !ok {
			t.Fatalf("응답이 어떤 그룹으로 집계된 소비량인지 말하지 않는다: %#v", body)
		}
		if effective != "user" {
			t.Errorf("consumption_group_by = %q, want %q", effective, "user")
		}
		row := consumptionForMarker(t, body)
		if row["group_by"] != "user" {
			t.Errorf("consumption 행이 user로 집계되지 않았다: %#v", row)
		}
		// 같은 응답의 trend는 project 라벨로 묶인다 — 두 축이 실제로 다르다.
		trend, _ := body["trend"].([]any)
		labelled := false
		for _, raw := range trend {
			point, _ := raw.(map[string]any)
			if point["group"] == project {
				labelled = true
				break
			}
		}
		if !labelled {
			t.Fatalf("trend가 project 라벨 %q로 묶이지 않았다 — 두 축이 다르다는 전제가 깨졌다", project)
		}
		for _, raw := range trend {
			point, _ := raw.(map[string]any)
			if point["group"] == marker {
				t.Errorf("trend가 username %q로 묶였다: %#v", marker, point)
			}
		}
	})

	t.Run("지원되는 그룹은 요청값을 그대로 돌려준다", func(t *testing.T) {
		for _, groupBy := range []string{"user", "hub", "network", "department"} {
			body := unwrap(call(t, "?group_by="+groupBy+"&granularity=hour"))
			if body["consumption_group_by"] != groupBy {
				t.Errorf("group_by=%s: consumption_group_by = %#v, want %q", groupBy, body["consumption_group_by"], groupBy)
			}
			row := consumptionForMarker(t, body)
			if row["group_by"] != groupBy {
				t.Errorf("group_by=%s: consumption 행의 group_by = %#v", groupBy, row["group_by"])
			}
		}
	})

	t.Run("빈 값과 문서 밖 값은 400이 아니라 user로 답한다", func(t *testing.T) {
		for _, query := range []string{"", "?group_by=", "?group_by=nonsense"} {
			body := unwrap(call(t, query))
			if body["consumption_group_by"] != "user" {
				t.Errorf("%q: consumption_group_by = %#v, want \"user\"", query, body["consumption_group_by"])
			}
			if row := consumptionForMarker(t, body); row["group_by"] != "user" {
				t.Errorf("%q: consumption 행의 group_by = %#v", query, row["group_by"])
			}
		}
	})

	// /usage/consumption은 이 과제의 범위 밖이다 — group_by=project도 계속 200에
	// user 그룹이고, 응답에 consumption_group_by 같은 새 키가 생기지 않아야 한다.
	t.Run("usage/consumption은 무변경", func(t *testing.T) {
		response := httptest.NewRecorder()
		server.usageConsumption(response, httptest.NewRequest("GET", "/api/v1/usage/consumption?group_by=project", nil))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		body := unwrap(payload)
		if _, added := body["consumption_group_by"]; added {
			t.Errorf("/usage/consumption 응답에 새 키가 생겼다: %#v", body)
		}
		rows, _ := body["data"].([]any)
		found := false
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if row["group"] == marker {
				found = true
				if row["group_by"] != "user" {
					t.Errorf("group_by = %#v, want \"user\"", row["group_by"])
				}
			}
		}
		if !found {
			t.Errorf("data에 %q 행이 없다: %#v", marker, rows)
		}
	})
}

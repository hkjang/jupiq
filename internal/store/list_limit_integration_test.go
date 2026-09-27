package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

// 상한을 넘는 limit은 상한으로 잘려야 한다. 기본값으로 떨어뜨리면 크게 요청한
// 쪽이 오히려 적게 받는다(?limit=500인 발송 기록이 200건이 아니라 50건). 세
// 상한은 openapi.yaml이 maximum으로 문서화한 값이고 pageBounds도 같은 규칙을
// 쓰므로, 여기서는 실제 PostgreSQL과 실제 Store로 세 함수를 그대로 호출해
// 확인한다.
func TestListLimitClampsToMaximumIntegration(t *testing.T) {
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
	defer database.Close()
	marker := fmt.Sprintf("limit-%d", time.Now().UnixNano())

	t.Run("mail deliveries", func(t *testing.T) {
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO mail_deliveries(event,recipient,subject,status)
			SELECT 'test', $1 || i || '@corp.internal', 'limit', 'sent'
			FROM generate_series(1, 201) AS i`, marker); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM mail_deliveries WHERE recipient LIKE $1`, marker+"%")
		}()
		// 상한(200) 이하의 요청은 지금과 같아야 하고, 상한을 넘는 요청은 상한까지
		// 돌려준다. 기본값 50은 limit을 주지 않았을 때만 나온다.
		for _, tc := range []struct{ limit, want int }{
			{0, 50}, {-1, 50}, {1, 1}, {50, 50}, {200, 200}, {201, 200}, {500, 200}, {1 << 30, 200},
		} {
			page, err := database.ListMailDeliveries(ctx, "", tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != tc.want {
				t.Errorf("ListMailDeliveries(limit=%d) returned %d items, want %d", tc.limit, len(page.Items), tc.want)
			}
		}
		// Summary·Total은 limit과 무관한 전체 집계다 — 이번 변경으로 움직이면 안 된다.
		small, err := database.ListMailDeliveries(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		large, err := database.ListMailDeliveries(ctx, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		if small.Total != large.Total || small.Total < 201 {
			t.Errorf("Total must count every row regardless of limit: %d vs %d", small.Total, large.Total)
		}
		if small.Summary["sent"] != large.Summary["sent"] || small.Summary["sent"] < 201 {
			t.Errorf("Summary must count every row regardless of limit: %v vs %v", small.Summary, large.Summary)
		}
	})

	t.Run("resource consumption", func(t *testing.T) {
		bucket := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO resource_usage_hourly(bucket,username,hub_name,cpu_core_seconds,runtime_seconds)
			SELECT $1::timestamptz, $2 || i, 'limit-hub', i, i
			FROM generate_series(1, 501) AS i`, bucket, marker); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM resource_usage_hourly WHERE bucket=$1`, bucket)
		}()
		from, to := bucket.Add(-time.Hour), bucket.Add(time.Hour)
		for _, tc := range []struct{ limit, want int }{
			{0, 100}, {-1, 100}, {1, 1}, {100, 100}, {500, 500}, {501, 500}, {1000, 500}, {1 << 30, 500},
		} {
			items, err := database.ResourceConsumption(ctx, from, to, "user", tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tc.want {
				t.Errorf("ResourceConsumption(limit=%d) returned %d groups, want %d", tc.limit, len(items), tc.want)
			}
		}
	})

	t.Run("metrics", func(t *testing.T) {
		metric := marker + "_probe_total"
		base := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO metric_samples(source,metric_name,labels,value,sampled_at)
			SELECT 'limit-test', $1, '{}'::jsonb, i, $2::timestamptz + (i * interval '1 second')
			FROM generate_series(1, 5001) AS i`, metric, base); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE metric_name=$1`, metric)
		}()
		from, to := base, base.Add(2*time.Hour)
		// openapi가 minimum: 0을 허용하므로 limit=0은 거부가 아니라 기본값 1000이다.
		for _, tc := range []struct{ limit, want int }{
			{0, 1000}, {-1, 1000}, {1, 1}, {1000, 1000}, {5000, 5000}, {5001, 5000}, {10000, 5000}, {1 << 30, 5000},
		} {
			items, blocked, err := database.Metrics(ctx, from, to, metric, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				t.Fatalf("Metrics(limit=%d) must not be treated as a blocked GPU metric", tc.limit)
			}
			if len(items) != tc.want {
				t.Errorf("Metrics(limit=%d) returned %d samples, want %d", tc.limit, len(items), tc.want)
			}
		}
	})
}

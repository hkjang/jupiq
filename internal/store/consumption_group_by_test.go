package store

import "testing"

// 소비량 집계는 resource_usage_hourly에 열이 있는 그룹만 알고, 모르는 값은 조용히
// user로 되돌린다. 호출자가 "무엇으로 묶인 결과인가"를 물을 수 있으려면 되돌림
// 규칙이 집계와 같은 표에서 나와야 하므로, 여기서 그 대응을 고정한다.
func TestConsumptionGroupByFallsBackToUserForUnknownGroups(t *testing.T) {
	for _, tc := range []struct {
		groupBy, want string
	}{
		{"user", "user"},
		{"hub", "hub"},
		{"network", "network"},
		{"department", "department"},
		// project는 /usage의 trend가 지원하지만 시간별 롤업에는 열이 없다.
		{"project", "user"},
		{"", "user"},
		{"nonsense", "user"},
		{"USER", "user"},
	} {
		t.Run(tc.groupBy, func(t *testing.T) {
			if got := ConsumptionGroupBy(tc.groupBy); got != tc.want {
				t.Fatalf("ConsumptionGroupBy(%q) = %q, want %q", tc.groupBy, got, tc.want)
			}
		})
	}
}

// 노출 함수와 집계가 같은 표를 보지 않으면 응답이 실제 집계와 다른 그룹을 말한다.
func TestConsumptionGroupByAgreesWithTheAggregatedColumns(t *testing.T) {
	for groupBy := range consumptionColumns {
		if got := ConsumptionGroupBy(groupBy); got != groupBy {
			t.Fatalf("ConsumptionGroupBy(%q) = %q, want the column's own group", groupBy, got)
		}
	}
	if _, ok := consumptionColumns["user"]; !ok {
		t.Fatal("되돌림 대상인 user가 집계 표에 없다")
	}
}

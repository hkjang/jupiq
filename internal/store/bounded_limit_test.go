package store

import (
	"fmt"
	"testing"
)

// 세 목록 함수가 쓰는 조합(발송 기록 50/200, 사용량 100/500, 지표 1000/5000)을
// 그대로 세운다. 상한 초과가 기본값으로 떨어지면 크게 요청한 쪽이 적게 받으므로,
// 초과는 반드시 상한이어야 한다.
func TestBoundedLimitClampsAboveTheMaximum(t *testing.T) {
	for _, group := range []struct {
		name              string
		fallback, maximum int
	}{
		{"mail deliveries", 50, 200},
		{"resource consumption", 100, 500},
		{"metrics", 1000, 5000},
	} {
		for _, tc := range []struct {
			limit, want int
		}{
			{0, group.fallback},
			{-1, group.fallback},
			{-group.maximum * 10, group.fallback},
			{1, 1},
			{group.fallback, group.fallback},
			{group.maximum - 1, group.maximum - 1},
			{group.maximum, group.maximum},
			{group.maximum + 1, group.maximum},
			{group.maximum * 10, group.maximum},
			{maxInt, group.maximum},
		} {
			t.Run(fmt.Sprintf("%s/%d", group.name, tc.limit), func(t *testing.T) {
				if got := boundedLimit(tc.limit, group.fallback, group.maximum); got != tc.want {
					t.Fatalf("boundedLimit(%d, %d, %d) = %d, want %d",
						tc.limit, group.fallback, group.maximum, got, tc.want)
				}
			})
		}
	}
}

// 세 기본값은 모두 상한 이하라 fallback을 먼저 적용해도 상한을 넘길 수 없다.
// 이 전제가 깨지면 "기본값을 달라"는 요청이 조용히 다른 값을 받는다.
func TestBoundedLimitFallbacksStayWithinTheirMaximum(t *testing.T) {
	for _, group := range [][2]int{{50, 200}, {100, 500}, {1000, 5000}} {
		if group[0] > group[1] {
			t.Fatalf("fallback %d exceeds maximum %d", group[0], group[1])
		}
		if got := boundedLimit(0, group[0], group[1]); got != group[0] {
			t.Fatalf("boundedLimit(0, %d, %d) = %d, want the fallback", group[0], group[1], got)
		}
	}
}

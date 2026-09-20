package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

// queryInt는 비어 있으면 fallback, 0 이상의 정수면 그 값, 그 밖(정수가 아님·음수·
// 공백 포함)은 오류다. 0과 상한 초과는 그대로 넘겨 store가 보정하게 둔다.
func TestQueryIntAcceptsOnlyNonNegativeIntegers(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 20},
		{raw: "7", want: 7},
		{raw: "0", want: 0},
		{raw: "999999", want: 999999},
		{raw: "abc", wantErr: true},
		{raw: "-1", wantErr: true},
		{raw: " 3", wantErr: true},
		{raw: "3.5", wantErr: true},
	}
	for _, test := range tests {
		t.Run("page_size="+test.raw, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/audit?page_size="+url.QueryEscape(test.raw), nil)
			got, err := queryInt(req, "page_size", 20)
			if test.wantErr {
				if err == nil {
					t.Fatalf("queryInt(%q) = %d, want error", test.raw, got)
				}
				if err.Error() != "page_size은(는) 0 이상의 정수여야 합니다" {
					t.Fatalf("unexpected message: %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("queryInt(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("queryInt(%q) = %d, want %d", test.raw, got, test.want)
			}
		})
	}
}

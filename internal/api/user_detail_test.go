package api

import (
	"net/http/httptest"
	"testing"
)

func TestUserDetailUsernameValidation(t *testing.T) {
	valid := []string{"user01", "john.doe", "user_name-01", "person@example.com"}
	for _, value := range valid {
		if !userDetailUsernamePattern.MatchString(value) {
			t.Errorf("expected valid username %q", value)
		}
	}
	invalid := []string{"", ".hidden", "space user", "slash/user", "한글사용자", "a" + string(make([]byte, 128))}
	for _, value := range invalid {
		if userDetailUsernamePattern.MatchString(value) {
			t.Errorf("expected invalid username %q", value)
		}
	}
}

func TestUserDetailQueryBounds(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/users/user01", nil)
	page, limit, include, err := userDetailQuery(req)
	if err != nil || page != 1 || limit != 50 || !include {
		t.Fatalf("unexpected defaults: page=%d limit=%d include=%v err=%v", page, limit, include, err)
	}

	req = httptest.NewRequest("GET", "/api/v1/users/user01?page=10000&limit=100&include_llm=false", nil)
	page, limit, include, err = userDetailQuery(req)
	if err != nil || page != 10000 || limit != 100 || include {
		t.Fatalf("unexpected upper bounds: page=%d limit=%d include=%v err=%v", page, limit, include, err)
	}

	for _, query := range []string{"page=0", "page=10001", "page=x", "limit=0", "limit=101", "limit=x", "include_llm=maybe"} {
		req = httptest.NewRequest("GET", "/api/v1/users/user01?"+query, nil)
		if _, _, _, err := userDetailQuery(req); err == nil {
			t.Errorf("expected %q to fail", query)
		}
	}
}

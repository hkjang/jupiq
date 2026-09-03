package auth

import "testing"

func TestSanitizeReturnToKeepsSameOriginPaths(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/dashboard", "/dashboard"},
		{"/users/user01?tab=servers", "/users/user01?tab=servers"},
		{"%2Fadmin%2Fsettings%3Ftab%3Dfeatures", "/admin/settings?tab=features"},
		{"/", "/"},
	} {
		if got := SanitizeReturnTo(tc.in); got != tc.want {
			t.Fatalf("SanitizeReturnTo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeReturnToRejectsOffSiteRedirects(t *testing.T) {
	for _, in := range []string{
		"",
		"https://evil.example/steal",
		"//evil.example/steal",
		"/\\evil.example",
		"http:/evil.example",
		"dashboard",
		"/dashboard\nSet-Cookie: a=b",
		"/dashboard\r\nLocation: https://evil.example",
	} {
		if got := SanitizeReturnTo(in); got != DefaultReturnTo {
			t.Fatalf("SanitizeReturnTo(%q) = %q, want %q", in, got, DefaultReturnTo)
		}
	}
}

func TestSanitizeReturnToRejectsOverlongPaths(t *testing.T) {
	long := "/" + string(make([]byte, maxReturnToLength))
	if got := SanitizeReturnTo(long); got != DefaultReturnTo {
		t.Fatalf("SanitizeReturnTo(long) = %q, want %q", got, DefaultReturnTo)
	}
}

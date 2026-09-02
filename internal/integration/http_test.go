package integration

import "testing"

func TestValidateEndpoint(t *testing.T) {
	for _, good := range []string{"https://jupyter.internal", "http://10.0.0.2:8000/base"} {
		if _, err := ValidateEndpoint(good); err != nil {
			t.Errorf("ValidateEndpoint(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"file:///etc/passwd", "https://user:password@example.com", "//missing-scheme", "javascript:alert(1)", "http://127.0.0.1:8080", "http://[::1]", "http://169.254.169.254", "http://metadata.google.internal"} {
		if _, err := ValidateEndpoint(bad); err == nil {
			t.Errorf("ValidateEndpoint(%q) accepted unsafe URL", bad)
		}
	}
}

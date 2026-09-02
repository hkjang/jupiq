package integration

import "testing"

func TestPodUsernamePattern(t *testing.T) {
	re, err := CompilePodUsernamePattern(`^jupyter-(?P<username>[a-zA-Z0-9._-]+)$`)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := UsernameFromPod(re, "jupyter-user01"); !ok || got != "user01" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, err := CompilePodUsernamePattern(`^jupyter-(.+)$`); err == nil {
		t.Fatal("expected named capture validation")
	}
}

func TestValidateLLMLabelMappingRejectsSensitiveAndInvalidNames(t *testing.T) {
	for _, remote := range []string{"authorization", "http_authorization_header", "user_prompt", "api_key", "9invalid", "unknown_label"} {
		if err := ValidateLLMLabelMapping("model", remote); err == nil {
			t.Fatalf("expected %q to be rejected", remote)
		}
	}
	if err := ValidateLLMLabelMapping("model", "served_model"); err != nil {
		t.Fatalf("safe mapping rejected: %v", err)
	}
	if err := ValidateLLMLabelMapping("unknown", "served_model"); err == nil {
		t.Fatal("unsupported canonical label must be rejected")
	}
}

func TestDecodeLLMLabelMappingsRequiresStringObject(t *testing.T) {
	if _, err := DecodeLLMLabelMappings(map[string]any{"model": 7}); err == nil {
		t.Fatal("non-string remote label must be rejected")
	}
	if _, err := DecodeLLMLabelMappings([]string{"model"}); err == nil {
		t.Fatal("non-object mappings must be rejected")
	}
}

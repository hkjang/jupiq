package api

import (
	"strings"
	"testing"
)

func TestValidateLLMUsageRejectsSensitiveLabelMapping(t *testing.T) {
	err := validateLLMUsage(map[string]any{
		"pod_username_regex": `^jupyter-(?P<username>[a-z0-9-]+)$`,
		"label_mappings":     map[string]any{"model": "authorization"},
	})
	if err == nil || !strings.Contains(err.Error(), "허용되지 않은") {
		t.Fatalf("sensitive mapping was not rejected: %v", err)
	}
}

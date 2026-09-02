package api

import (
	"testing"
	"time"
)

func TestAPIKeyPolicyAppliesRotationAndPermissionLimits(t *testing.T) {
	var expires *time.Time
	policy := apiKeyPolicy{RotationDays: 30, MaxLifetimeDays: 90, Permissions: []string{"dashboard:read", "usage:*"}}
	if err := applyAPIKeyPolicy(policy, []string{"dashboard:read"}, &expires); err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(*expires)
	if remaining < 29*24*time.Hour || remaining > 31*24*time.Hour {
		t.Fatalf("unexpected default rotation expiry: %s", remaining)
	}
	if err := applyAPIKeyPolicy(policy, []string{"settings:write"}, &expires); err == nil {
		t.Fatal("scope outside administrator allowlist was accepted")
	}
}

func TestAPIKeyPolicyRejectsExcessLifetime(t *testing.T) {
	value := time.Now().UTC().Add(91 * 24 * time.Hour)
	expires := &value
	if err := applyAPIKeyPolicy(apiKeyPolicy{RotationDays: 30, MaxLifetimeDays: 90}, []string{"dashboard:read"}, &expires); err == nil {
		t.Fatal("expiry beyond maximum lifetime was accepted")
	}
}

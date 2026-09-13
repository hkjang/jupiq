package api

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAndExpires(t *testing.T) {
	limiter := newLoginLimiter()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for i := 0; i < loginAttemptLimit; i++ {
		if !limiter.allow("192.0.2.1", "Admin") {
			t.Fatalf("attempt %d blocked too early", i+1)
		}
		limiter.failed("192.0.2.1", "Admin")
	}
	if limiter.allow("192.0.2.1", "admin") {
		t.Fatal("normalized account should be rate limited")
	}
	now = now.Add(loginAttemptWindow)
	if !limiter.allow("192.0.2.1", "admin") {
		t.Fatal("expired login window remained blocked")
	}
}

func TestLoginLimiterSuccessClearsFailures(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginAttemptLimit; i++ {
		limiter.failed("192.0.2.2", "user")
	}
	limiter.succeeded("192.0.2.2", "user")
	if !limiter.allow("192.0.2.2", "user") {
		t.Fatal("successful authentication did not reset limiter")
	}
}

func TestLoginLimiterBlocksUsernameSprayByIP(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginIPLimit; i++ {
		username := fmt.Sprintf("spray-%d", i)
		if !limiter.allow("192.0.2.9", username) {
			t.Fatalf("IP blocked too early at %d", i)
		}
		limiter.failed("192.0.2.9", username)
	}
	if limiter.allow("192.0.2.9", "another-user") {
		t.Fatal("IP-wide username spray was not blocked")
	}
}

func TestLoginLimiterSuccessKeepsIPWindow(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginIPLimit; i++ {
		limiter.failed("192.0.2.10", fmt.Sprintf("spray-%d", i))
	}
	// A valid login for one account must not lift the address-wide block:
	// the sprayer would otherwise reset its budget with a single real account.
	limiter.succeeded("192.0.2.10", "spray-0")
	if limiter.allow("192.0.2.10", "spray-0") {
		t.Fatal("success cleared the IP-wide window")
	}
	if limiter.allow("192.0.2.10", "victim") {
		t.Fatal("success let the address keep spraying other accounts")
	}
	if !limiter.allow("192.0.2.11", "spray-0") {
		t.Fatal("account window survived a successful login")
	}
}

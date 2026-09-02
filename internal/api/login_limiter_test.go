package api

import (
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

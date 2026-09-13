package api

import (
	"fmt"
	"testing"
	"time"
)

func TestRequestLimiterCountsEveryHitAndResetsAfterWindow(t *testing.T) {
	limiter := newRequestLimiter(time.Minute, 3)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !limiter.allow("192.0.2.1") {
			t.Fatalf("hit %d blocked too early", i+1)
		}
	}
	if limiter.allow("192.0.2.1") {
		t.Fatal("fourth hit inside the window was allowed")
	}
	if !limiter.allow("192.0.2.2") {
		t.Fatal("another address was blocked by the first one's window")
	}
	// Blocked hits must not extend the window: it started at the first hit.
	now = now.Add(30 * time.Second)
	if limiter.allow("192.0.2.1") {
		t.Fatal("hit halfway through the window was allowed")
	}
	now = now.Add(30 * time.Second)
	if !limiter.allow("192.0.2.1") {
		t.Fatal("window did not reset one minute after the first hit")
	}
}

func TestRequestLimiterPrunesWhenFull(t *testing.T) {
	limiter := newRequestLimiter(time.Minute, 1)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for i := 0; i < requestLimiterMax; i++ {
		limiter.allow(fmt.Sprintf("198.51.100.%d", i))
	}
	if len(limiter.hits) != requestLimiterMax {
		t.Fatalf("hits=%d want %d", len(limiter.hits), requestLimiterMax)
	}
	// All windows are live, so the oldest entry is evicted to make room.
	if !limiter.allow("203.0.113.1") {
		t.Fatal("new address blocked while the map was being pruned")
	}
	if len(limiter.hits) > requestLimiterMax {
		t.Fatalf("map grew past the cap: %d", len(limiter.hits))
	}
	// A minute later every window has expired, so only the newcomer remains.
	now = now.Add(time.Minute)
	limiter.allow("203.0.113.2")
	if len(limiter.hits) != 1 {
		t.Fatalf("expired windows were not dropped once the cap was reached: %d", len(limiter.hits))
	}
}

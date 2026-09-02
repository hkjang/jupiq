package store

import (
	"testing"
	"time"
)

func TestSessionSnapshotStaleUsesCollectionInterval(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if sessionSnapshotStale(now, now.Add(-4*time.Minute), 60) {
		t.Fatal("fresh 60-second snapshot marked stale")
	}
	if !sessionSnapshotStale(now, now.Add(-6*time.Minute), 60) {
		t.Fatal("old snapshot counted as live")
	}
	if sessionSnapshotStale(now, now.Add(-15*time.Minute), 600) {
		t.Fatal("10-minute collector should receive a 20-minute freshness window")
	}
	if !sessionSnapshotStale(now, now.Add(-21*time.Minute), 600) {
		t.Fatal("snapshot beyond twice the collection interval counted as live")
	}
}

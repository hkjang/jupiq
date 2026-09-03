package integration

import (
	"math"
	"testing"
	"time"
)

func TestValidPrometheusSampleRejectsNonFiniteNegativeAndInvalidTime(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	validTimestamp := float64(now.Unix())
	for _, test := range []struct {
		name      string
		timestamp float64
		value     float64
	}{
		{"NaN", validTimestamp, math.NaN()},
		{"Inf", validTimestamp, math.Inf(1)},
		{"negative", validTimestamp, -1},
		{"old timestamp", 1, 1},
		{"future timestamp", float64(now.Add(10 * time.Minute).Unix()), 1},
	} {
		if validPrometheusSample(test.timestamp, test.value, now) {
			t.Errorf("%s sample was accepted", test.name)
		}
	}
	if !validPrometheusSample(validTimestamp, 42, now) {
		t.Fatal("valid sample was rejected")
	}
}

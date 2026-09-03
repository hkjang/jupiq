package store

import (
	"math"
	"testing"
)

func TestLLMDimensionFingerprintIsStableAndDimensionAware(t *testing.T) {
	left := llmDimensionFingerprint("user01", "jupyter-user01", map[string]string{"model": "model-a", "status": "200", "path": "/v1/chat/completions"})
	right := llmDimensionFingerprint("user01", "jupyter-user01", map[string]string{"path": "/v1/chat/completions", "status": "200", "model": "model-a"})
	if left != right || left == "" {
		t.Fatalf("fingerprint is not stable: %q %q", left, right)
	}
	changed := llmDimensionFingerprint("user01", "jupyter-user01", map[string]string{"model": "model-b", "status": "200", "path": "/v1/chat/completions"})
	if changed == left {
		t.Fatal("model dimension did not change fingerprint")
	}
}

func TestRoundedMetricCountUsesNearestWholeUnit(t *testing.T) {
	for _, test := range []struct {
		value float64
		want  int64
	}{{0.49, 0}, {0.5, 1}, {1.51, 2}, {42, 42}} {
		got, ok := roundedMetricCount(test.value)
		if !ok || got != test.want {
			t.Fatalf("roundedMetricCount(%v)=(%d,%t) want (%d,true)", test.value, got, ok, test.want)
		}
	}
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1), float64(math.MaxInt64)} {
		if _, ok := roundedMetricCount(invalid); ok {
			t.Fatalf("roundedMetricCount(%v) accepted invalid value", invalid)
		}
	}
}

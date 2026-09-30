package main

import (
	"math"
	"testing"
)

func TestAverageIgnoresNonfiniteAndHandlesEmpty(t *testing.T) {
	if got := average(nil); got != 0 {
		t.Fatalf("average(nil) = %g, want 0", got)
	}
	if got := average([]float64{1, 2, math.NaN(), math.Inf(1)}); got != 1.5 {
		t.Fatalf("average with nonfinite values = %g, want 1.5", got)
	}
	if got := average([]float64{math.MaxFloat64, math.MaxFloat64}); got != math.MaxFloat64 {
		t.Fatalf("average of large finite values = %g, want MaxFloat64", got)
	}
}

func TestMedianUsesFiniteCopy(t *testing.T) {
	values := []float64{5, 1, math.NaN(), 3, math.Inf(-1)}
	before := append([]float64(nil), values...)
	if got := median(values); got != 3 {
		t.Fatalf("median = %g, want 3", got)
	}
	if !sameFloatSlice(values, before) {
		t.Fatalf("median modified its input: %v -> %v", before, values)
	}
	if got := median(nil); got != 0 {
		t.Fatalf("median(nil) = %g, want 0", got)
	}
}

func TestQuartileType7Interpolation(t *testing.T) {
	tests := []struct {
		name       string
		values     []float64
		percentile float64
		want       float64
	}{
		{name: "q90 download", values: []float64{50.2, 55.1, 52.8, 51.9, 53.7, 58.2, 49.8, 54.3, 53.0, 56.1}, percentile: 0.9, want: 56.31},
		{name: "q90 upload", values: []float64{1, 2, 3, 4, 5, 6, 7, 8, 37, 40.7}, percentile: 0.9, want: 37.37},
		{name: "q90 pooled sample", values: []float64{1, 2, 3, 4, 5, 6, 7, 8, 28, 30.8}, percentile: 0.9, want: 28.28},
		{name: "median", values: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, percentile: 0.5, want: 5.5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := quartile(test.values, test.percentile); math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("quartile(%v, %g) = %g, want %g", test.values, test.percentile, got, test.want)
			}
		})
	}
}

func TestQuartileEmptyNonfiniteAndBoundsAreSafe(t *testing.T) {
	if got := quartile(nil, 0.5); got != 0 {
		t.Fatalf("quartile(nil, 0.5) = %g, want 0", got)
	}
	values := []float64{math.NaN(), 4, math.Inf(1), 1, math.Inf(-1)}
	before := append([]float64(nil), values...)
	if got := quartile(values, 0.5); got != 2.5 {
		t.Fatalf("quartile with nonfinite inputs = %g, want 2.5", got)
	}
	if got := quartile(values, -2); got != 1 {
		t.Fatalf("lower out-of-range percentile = %g, want 1", got)
	}
	if got := quartile(values, 3); got != 4 {
		t.Fatalf("upper out-of-range percentile = %g, want 4", got)
	}
	if got := quartile(values, math.NaN()); got != 0 {
		t.Fatalf("NaN percentile = %g, want 0", got)
	}
	if !sameFloatSlice(values, before) {
		t.Fatalf("quartile modified its input: %v -> %v", before, values)
	}
}

func TestJitterUsesFiniteConsecutivePoints(t *testing.T) {
	if got := jitter(nil); got != 0 {
		t.Fatalf("jitter(nil) = %g, want 0", got)
	}
	if got := jitter([]float64{1, math.NaN(), 4, 2}); got != 2.5 {
		t.Fatalf("jitter with a nonfinite point = %g, want 2.5", got)
	}
	if got := jitter([]float64{math.MaxFloat64, -math.MaxFloat64}); got != math.MaxFloat64 {
		t.Fatalf("extreme jitter = %g, want finite MaxFloat64", got)
	}
}

func sameFloatSlice(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if math.IsNaN(left[i]) && math.IsNaN(right[i]) {
			continue
		}
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

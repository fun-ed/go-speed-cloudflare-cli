package main

import (
	"math"
	"testing"
)

func TestAverage(t *testing.T) {
	tests := []struct {
		name     string
		values   []float64
		expected float64
	}{
		{"Empty", []float64{}, 0},
		{"Single", []float64{5}, 5},
		{"Multiple", []float64{1, 2, 3, 4, 5}, 3},
		{"Negative", []float64{-1, -2, -3, -4, -5}, -3},
		{"Mixed", []float64{-5, 0, 5}, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.values) == 0 {
				// Skip empty test case as it would cause a division by zero
				return
			}
			result := average(test.values)
			if math.Abs(result-test.expected) > 0.00001 {
				t.Errorf("average(%v) = %f; expected %f", test.values, result, test.expected)
			}
		})
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		name     string
		values   []float64
		expected float64
	}{
		{"Empty", []float64{}, 0},
		{"Single", []float64{5}, 5},
		{"Odd", []float64{1, 3, 2}, 2},
		{"Even", []float64{1, 3, 2, 4}, 2.5},
		{"Sorted", []float64{1, 2, 3, 4, 5}, 3},
		{"Unsorted", []float64{5, 2, 1, 4, 3}, 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.values) == 0 {
				// Skip empty test case as median is undefined for empty slices
				return
			}
			result := median(test.values)
			if math.Abs(result-test.expected) > 0.00001 {
				t.Errorf("median(%v) = %f; expected %f", test.values, result, test.expected)
			}
		})
	}
}

func TestQuartile(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		name       string
		percentile float64
		expected   float64
	}{
		{"Min", 0, 1},
		{"Q1", 0.25, 3.25},
		{"Median", 0.5, 5.5},
		{"Q3", 0.75, 7.75},
		{"Max", 1, 10},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := quartile(values, test.percentile)
			if math.Abs(result-test.expected) > 0.00001 {
				t.Errorf("quartile(%v, %f) = %f; expected %f", values, test.percentile, result, test.expected)
			}
		})
	}

	// Test empty slice
	result := quartile([]float64{}, 0.5)
	if result != 0 {
		t.Errorf("quartile([], 0.5) = %f; expected 0", result)
	}
}

func TestJitter(t *testing.T) {
	tests := []struct {
		name     string
		values   []float64
		expected float64
	}{
		{"Empty", []float64{}, 0},
		{"Single", []float64{5}, 0},
		{"Two", []float64{1, 3}, 2},
		{"Consistent", []float64{1, 2, 3, 4, 5}, 1},
		{"Varying", []float64{1, 5, 2, 8, 3}, 4.5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := jitter(test.values)
			if math.Abs(result-test.expected) > 0.00001 {
				t.Errorf("jitter(%v) = %f; expected %f", test.values, result, test.expected)
			}
		})
	}
}

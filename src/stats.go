package main

import (
	"math"
	"sort"
)

func average(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	half := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[half]
	}
	return (sorted[half-1] + sorted[half]) / 2
}

func quartile(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	pos := (float64(len(sorted) - 1)) * percentile
	base := int(math.Floor(pos))
	rest := pos - float64(base)
	if base+1 < len(sorted) {
		return sorted[base] + rest*(sorted[base+1]-sorted[base])
	}
	return sorted[base]
}

func jitter(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	jitters := make([]float64, 0, len(values)-1)
	for i := 0; i < len(values)-1; i++ {
		jitters = append(jitters, math.Abs(values[i]-values[i+1]))
	}
	return average(jitters)
}

package main

import (
	"math"
	"sort"
)

func average(values []float64) float64 {
	finite := finiteValues(values)
	if len(finite) == 0 {
		return 0
	}

	maxAbs := 0.0
	for _, value := range finite {
		maxAbs = math.Max(maxAbs, math.Abs(value))
	}
	if maxAbs == 0 {
		return 0
	}

	scaledTotal := 0.0
	for _, value := range finite {
		scaledTotal += value / maxAbs
	}
	scaledMean := scaledTotal / float64(len(finite))
	if scaledMean > 1 {
		scaledMean = 1
	} else if scaledMean < -1 {
		scaledMean = -1
	}
	return scaledMean * maxAbs
}

func median(values []float64) float64 {
	sorted := finiteValues(values)
	if len(sorted) == 0 {
		return 0
	}
	sort.Float64s(sorted)
	half := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[half]
	}
	return sorted[half-1]/2 + sorted[half]/2
}

func quartile(values []float64, percentile float64) float64 {
	sorted := finiteValues(values)
	if len(sorted) == 0 || math.IsNaN(percentile) {
		return 0
	}
	sort.Float64s(sorted)
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}

	pos := float64(len(sorted)-1) * percentile
	base := int(math.Floor(pos))
	rest := pos - float64(base)
	if base+1 >= len(sorted) {
		return sorted[base]
	}
	return sorted[base]*(1-rest) + sorted[base+1]*rest
}

func jitter(values []float64) float64 {
	finite := finiteValues(values)
	if len(finite) < 2 {
		return 0
	}
	jitters := make([]float64, 0, len(finite)-1)
	for i := range finite[:len(finite)-1] {
		difference := math.Abs(finite[i] - finite[i+1])
		if math.IsInf(difference, 0) {
			difference = math.MaxFloat64
		}
		jitters = append(jitters, difference)
	}
	return average(jitters)
}

func finiteValues(values []float64) []float64 {
	finite := make([]float64, 0, len(values))
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			finite = append(finite, value)
		}
	}
	return finite
}

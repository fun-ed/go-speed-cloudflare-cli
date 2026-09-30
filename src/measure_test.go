package main

import (
	"math"
	"testing"
	"time"
)

func TestBandwidthReducerPoolsEligibleFiniteSamples(t *testing.T) {
	values := []float64{50.2, 55.1, 52.8, 51.9, 53.7, 58.2, 49.8, 54.3, 53.0, 56.1}
	samples := make([]sample, 0, len(values)+3)
	for _, speed := range values {
		samples = append(samples, sample{SpeedBps: speed, DurationMs: 10})
	}
	samples = append(samples,
		sample{SpeedBps: 999, DurationMs: 9.99},
		sample{SpeedBps: math.NaN(), DurationMs: 20},
		sample{SpeedBps: math.Inf(1), DurationMs: 20},
	)
	before := append([]sample(nil), samples...)
	got, ok := bandwidthBps(samples)
	if !ok || math.Abs(got-56.31) > 1e-12 {
		t.Fatalf("bandwidthBps = %g, %v; want 56.31, true", got, ok)
	}
	for i := range samples {
		current, previous := samples[i], before[i]
		if math.Float64bits(current.SpeedBps) != math.Float64bits(previous.SpeedBps) {
			t.Fatalf("bandwidth reducer changed sample %d speed", i)
		}
		current.SpeedBps, previous.SpeedBps = 0, 0
		if current != previous {
			t.Fatalf("bandwidth reducer changed sample %d", i)
		}
	}
	if got, ok := bandwidthBps([]sample{{SpeedBps: 10, DurationMs: 9.999}}); ok || got != 0 {
		t.Fatalf("short-only bucket = %g, %v; want 0, false", got, ok)
	}
}

func TestLatencyReducerPreservesChronologicalJitterOrder(t *testing.T) {
	samples := []sample{
		{LatencyMs: 1},
		{LatencyMs: 4},
		{LatencyMs: math.NaN()},
		{LatencyMs: 2},
		{LatencyMs: math.Inf(1)},
	}
	latency, jitter, latencyOK, jitterOK := latencyStats(samples)
	if !latencyOK || !jitterOK || latency != 2 || jitter != 2.5 {
		t.Fatalf("latencyStats = %g, %g, %v, %v", latency, jitter, latencyOK, jitterOK)
	}
	if _, _, latencyOK, jitterOK := latencyStats(nil); latencyOK || jitterOK {
		t.Fatalf("empty latency stats marked valid: latency=%v jitter=%v", latencyOK, jitterOK)
	}
	if _, _, latencyOK, jitterOK := latencyStats([]sample{{LatencyMs: 5}}); !latencyOK || jitterOK {
		t.Fatalf("single ping validity: latency=%v jitter=%v", latencyOK, jitterOK)
	}
}

func TestLoadedLatencyUsesEligibleBucketsAndLatestChronologicalPoints(t *testing.T) {
	start := time.Unix(100, 0)
	results := []phaseResult{
		{
			Phase:         phase{Direction: measurementDownload, Bytes: 100_000},
			Samples:       []sample{{DurationMs: 300}},
			LoadedLatency: []sample{{LatencyMs: 999, Started: start.Add(time.Minute)}},
		},
		{
			Phase:   phase{Direction: measurementDownload, Bytes: 100_000},
			Samples: []sample{{DurationMs: 249}},
			LoadedLatency: []sample{
				{LatencyMs: 998, Started: start.Add(2 * time.Minute)},
			},
		},
		{
			Phase:   phase{Direction: measurementDownload, Bytes: 200_000},
			Samples: []sample{{DurationMs: 300}},
			LoadedLatency: []sample{
				{LatencyMs: 20, Started: start.Add(20 * time.Second)},
				{LatencyMs: 0, Started: start},
			},
		},
		{
			Phase:   phase{Direction: measurementDownload, Bytes: 1_000_000},
			Samples: []sample{{DurationMs: 249}},
			LoadedLatency: []sample{
				{LatencyMs: 997, Started: start.Add(3 * time.Minute)},
			},
		},
		{
			Phase:         phase{Direction: measurementUpload, Bytes: 100_000},
			Samples:       []sample{{DurationMs: 500}},
			LoadedLatency: []sample{{LatencyMs: 500, Started: start.Add(time.Hour)}},
		},
	}
	for index := 19; index >= 1; index-- {
		results[2].LoadedLatency = append(results[2].LoadedLatency, sample{
			LatencyMs: float64(index),
			Started:   start.Add(time.Duration(index) * time.Second),
		})
	}
	latency, jitter, latencyOK, jitterOK := loadedLatencyStats(results, measurementDownload)
	if !latencyOK || !jitterOK || latency != 10.5 || jitter != 1 {
		t.Fatalf("loaded stats = %g, %g, %v, %v", latency, jitter, latencyOK, jitterOK)
	}
}

func TestShouldFinishRequiresAllSuccessfulSamplesOverThreshold(t *testing.T) {
	result := phaseResult{
		Phase: phase{Direction: measurementDownload, Bytes: 100_000},
		Samples: []sample{
			{DurationMs: 1500},
			{DurationMs: 1001},
		},
	}
	if !shouldFinish(result) {
		t.Fatal("slow completed phase should finish this direction")
	}
	result.Samples = append(result.Samples, sample{DurationMs: 1000})
	if shouldFinish(result) {
		t.Fatal("a phase with a 1000ms minimum must not finish")
	}
	result.Phase.BypassFinish = true
	if shouldFinish(result) {
		t.Fatal("bypass phase must not finish")
	}
	if shouldFinish(phaseResult{Phase: phase{Direction: measurementDownload}}) {
		t.Fatal("empty phase must not finish")
	}
}

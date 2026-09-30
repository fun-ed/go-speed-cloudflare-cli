package main

import (
	"math"
	"reflect"
	"testing"
)

func TestNetworkQualityScoresOfficialExample(t *testing.T) {
	summary := qualitySummary{
		DownloadBps:         metric(50e6),
		UploadBps:           metric(10e6),
		LatencyMs:           metric(20),
		JitterMs:            metric(10),
		DownLoadedLatencyMs: metric(50),
		UpLoadedLatencyMs:   metric(40),
		PacketLoss:          metric(0.01),
	}

	got := networkQualityScores(summary)
	want := []qualityScore{
		{Name: "streaming", Points: 35, Classification: "average", Available: true},
		{Name: "gaming", Points: 15, Classification: "average", Available: true},
		{Name: "rtc", Points: 20, Classification: "average", Available: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("networkQualityScores(official example) = %#v, want %#v", got, want)
	}
}

func TestMetricThresholdBoundaryMaps(t *testing.T) {
	tests := []struct {
		name  string
		scale metricScale
	}{
		{name: "packet loss", scale: packetLossScale},
		{name: "latency", scale: latencyScale},
		{name: "loaded latency increase", scale: latencyScale},
		{name: "jitter", scale: jitterScale},
		{name: "download", scale: downloadScale},
		{name: "upload", scale: uploadScale},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for index, threshold := range test.scale.thresholds {
				below := math.Nextafter(threshold, math.Inf(-1))
				above := math.Nextafter(threshold, math.Inf(1))
				if got := metricPoints(below, test.scale); got != test.scale.points[index] {
					t.Errorf("points just below %g = %d, want %d", threshold, got, test.scale.points[index])
				}
				if got := metricPoints(threshold, test.scale); got != test.scale.points[index+1] {
					t.Errorf("points at %g = %d, want %d (equality advances)", threshold, got, test.scale.points[index+1])
				}
				if got := metricPoints(above, test.scale); got != test.scale.points[index+1] {
					t.Errorf("points just above %g = %d, want %d", threshold, got, test.scale.points[index+1])
				}
			}
			if got := metricPoints(math.MaxFloat64, test.scale); got != test.scale.points[len(test.scale.points)-1] {
				t.Errorf("points above final threshold = %d, want %d", got, test.scale.points[len(test.scale.points)-1])
			}
		})
	}
}

func TestClassificationThresholdBoundaryMaps(t *testing.T) {
	tests := []struct {
		name       string
		thresholds []int
	}{
		{name: "streaming", thresholds: []int{15, 20, 40, 60}},
		{name: "gaming", thresholds: []int{5, 15, 25, 30}},
		{name: "rtc", thresholds: []int{5, 15, 25, 40}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for index, threshold := range test.thresholds {
				below := makeQualityScore(test.name, threshold-1, true, test.thresholds)
				if want := classifications[index]; below.Classification != want {
					t.Errorf("classification below %d = %q, want %q", threshold, below.Classification, want)
				}
				equal := makeQualityScore(test.name, threshold, true, test.thresholds)
				if want := classifications[index+1]; equal.Classification != want {
					t.Errorf("classification at %d = %q, want %q (equality advances)", threshold, equal.Classification, want)
				}
				above := makeQualityScore(test.name, threshold+1, true, test.thresholds)
				if want := classifications[index+1]; above.Classification != want {
					t.Errorf("classification above %d = %q, want %q", threshold, above.Classification, want)
				}
			}
		})
	}
}

func TestNetworkQualityScoresReachEveryCategoryBoundary(t *testing.T) {
	tests := []struct {
		name       string
		experience string
		summary    qualitySummary
		wantPoint  int
		wantClass  string
	}{
		{name: "streaming poor", experience: "streaming", summary: summaryFor(20, 20, metric(0.01), 1, 0), wantPoint: 15, wantClass: "poor"},
		{name: "streaming average", experience: "streaming", summary: summaryFor(20, 20, metric(0.01), 1e6, 0), wantPoint: 20, wantClass: "average"},
		{name: "streaming good", experience: "streaming", summary: summaryFor(20, 20, metric(0.05), 100e6, 0), wantPoint: 40, wantClass: "good"},
		{name: "streaming great", experience: "streaming", summary: summaryFor(5, 5, metric(0), 10e6, 0), wantPoint: 60, wantClass: "great"},
		{name: "gaming poor", experience: "gaming", summary: summaryFor(20, 100, metric(0), 0, 0), wantPoint: 5, wantClass: "poor"},
		{name: "gaming average", experience: "gaming", summary: summaryFor(20, 20, metric(0.01), 0, 0), wantPoint: 15, wantClass: "average"},
		{name: "gaming good", experience: "gaming", summary: summaryFor(10, 10, metric(0.01), 0, 0), wantPoint: 25, wantClass: "good"},
		{name: "gaming great", experience: "gaming", summary: summaryFor(5, 10, optionalMetric{}, 0, 0), wantPoint: 30, wantClass: "great"},
		{name: "rtc poor", experience: "rtc", summary: summaryFor(20, 100, metric(0), 0, 20), wantPoint: 5, wantClass: "poor"},
		{name: "rtc average", experience: "rtc", summary: summaryFor(20, 20, metric(0.01), 0, 20), wantPoint: 15, wantClass: "average"},
		{name: "rtc good", experience: "rtc", summary: summaryFor(20, 20, metric(0.01), 0, 9), wantPoint: 25, wantClass: "good"},
		{name: "rtc great", experience: "rtc", summary: summaryFor(5, 5, metric(0.05), 0, 20), wantPoint: 40, wantClass: "great"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := scoreByName(networkQualityScores(test.summary), test.experience)
			if !got.Available || got.Points != test.wantPoint || got.Classification != test.wantClass {
				t.Fatalf("score = %#v, want %d points classified %q", got, test.wantPoint, test.wantClass)
			}
		})
	}
}

func TestNetworkQualityScoresClampOnlyBelowZero(t *testing.T) {
	summary := summaryFor(501, 501, metric(1), 1, 501)
	for _, score := range networkQualityScores(summary) {
		if !score.Available || score.Points != 0 || score.Classification != "bad" {
			t.Errorf("low score = %#v, want available zero-point bad score", score)
		}
	}

	// There is no upper clamp to a normalized percentage or category threshold.
	high := summaryFor(0, 0, metric(0), 100e6, 0)
	got := scoreByName(networkQualityScores(high), "streaming")
	if got.Points != 80 || got.Classification != "great" {
		t.Fatalf("high streaming score = %#v, want 80 points classified great", got)
	}
}

func TestMissingPacketLossIsNeutralAndDiffersFromMeasuredZero(t *testing.T) {
	missing := summaryFor(20, 30, optionalMetric{}, 50e6, 10)
	measuredZero := missing
	measuredZero.PacketLoss = metric(0)

	missingScores := networkQualityScores(missing)
	zeroScores := networkQualityScores(measuredZero)
	for index := range missingScores {
		if !missingScores[index].Available || !zeroScores[index].Available {
			t.Fatalf("missing-loss/zero-loss results should be available: %#v / %#v", missingScores[index], zeroScores[index])
		}
		if delta := zeroScores[index].Points - missingScores[index].Points; delta != 10 {
			t.Errorf("%s zero-loss bonus = %d points, want 10", missingScores[index].Name, delta)
		}
	}
}

func TestNetworkQualityScoresAvailabilityForMissingInputs(t *testing.T) {
	base := summaryFor(20, 30, metric(0.01), 50e6, 10)
	tests := []struct {
		name      string
		summary   qualitySummary
		available [3]bool
	}{
		{name: "missing download only omits streaming", summary: setMetric(base, "download", optionalMetric{}), available: [3]bool{false, true, true}},
		{name: "missing jitter only omits rtc", summary: setMetric(base, "jitter", optionalMetric{}), available: [3]bool{true, true, false}},
		{name: "only download-loaded latency is missing", summary: setMetric(base, "download-loaded", optionalMetric{}), available: [3]bool{}},
		{name: "only upload-loaded latency is missing", summary: setMetric(base, "upload-loaded", optionalMetric{}), available: [3]bool{}},
		{name: "missing idle latency omits all", summary: setMetric(base, "latency", optionalMetric{}), available: [3]bool{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := networkQualityScores(test.summary)
			for index, score := range got {
				if score.Available != test.available[index] {
					t.Errorf("%s availability = %v, want %v", score.Name, score.Available, test.available[index])
				}
				if !score.Available && score.Classification != "unavailable" {
					t.Errorf("%s unavailable classification = %q, want unavailable", score.Name, score.Classification)
				}
			}
		})
	}
}

func TestNetworkQualityScoresValidateRangesAndNonfiniteInputs(t *testing.T) {
	base := summaryFor(20, 30, metric(0.01), 50e6, 10)
	tests := []struct {
		name      string
		summary   qualitySummary
		available [3]bool
	}{
		{name: "negative latency", summary: setMetric(base, "latency", metric(-1)), available: [3]bool{}},
		{name: "NaN latency", summary: setMetric(base, "latency", metric(math.NaN())), available: [3]bool{}},
		{name: "infinite latency", summary: setMetric(base, "latency", metric(math.Inf(1))), available: [3]bool{}},
		{name: "negative loaded latency", summary: setMetric(base, "download-loaded", metric(-1)), available: [3]bool{}},
		{name: "nonfinite loaded latency", summary: setMetric(base, "upload-loaded", metric(math.Inf(1))), available: [3]bool{}},
		{name: "negative download", summary: setMetric(base, "download", metric(-1)), available: [3]bool{false, true, true}},
		{name: "NaN download", summary: setMetric(base, "download", metric(math.NaN())), available: [3]bool{false, true, true}},
		{name: "zero download", summary: setMetric(base, "download", metric(0)), available: [3]bool{false, true, true}},
		{name: "negative jitter", summary: setMetric(base, "jitter", metric(-1)), available: [3]bool{true, true, false}},
		{name: "infinite jitter", summary: setMetric(base, "jitter", metric(math.Inf(-1))), available: [3]bool{true, true, false}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for index, score := range networkQualityScores(test.summary) {
				if score.Available != test.available[index] {
					t.Errorf("%s availability = %v, want %v", score.Name, score.Available, test.available[index])
				}
			}
		})
	}
}

func TestInvalidPacketLossIsNeutralWithoutNonfiniteBonus(t *testing.T) {
	base := summaryFor(20, 30, optionalMetric{}, 50e6, 10)
	want := networkQualityScores(base)
	invalid := []optionalMetric{
		metric(-0.01),
		metric(1.01),
		metric(math.NaN()),
		metric(math.Inf(1)),
	}
	for _, value := range invalid {
		got := networkQualityScores(setMetric(base, "loss", value))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("invalid packet loss %#v produced %#v, want neutral missing-loss result %#v", value, got, want)
		}
	}
}

func TestRealZeroLatenciesAreValidAndNegativeLoadedIncreaseUsesBestBucket(t *testing.T) {
	summary := summaryFor(0, 0, optionalMetric{}, 50e6, 0)
	got := networkQualityScores(summary)
	want := []qualityScore{
		{Name: "streaming", Points: 60, Classification: "great", Available: true},
		{Name: "gaming", Points: 40, Classification: "great", Available: true},
		{Name: "rtc", Points: 50, Classification: "great", Available: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scores with real zero latencies = %#v, want %#v", got, want)
	}

	negativeIncrease := summaryFor(20, -5, metric(0.05), 0, 100)
	if scores := networkQualityScores(negativeIncrease); !scoreByName(scores, "gaming").Available || scoreByName(scores, "gaming").Points != 25 {
		t.Fatalf("negative loaded increase did not use best bucket: %#v", scoreByName(scores, "gaming"))
	}
}

func TestUploadDoesNotAffectDefaultExperienceScores(t *testing.T) {
	summary := summaryFor(20, 30, metric(0.01), 50e6, 10)
	before := networkQualityScores(summary)
	summary.UploadBps = metric(math.MaxFloat64)
	if after := networkQualityScores(summary); !reflect.DeepEqual(after, before) {
		t.Fatalf("changing upload throughput changed default experience scores: %#v -> %#v", before, after)
	}
}

func TestNetworkQualityScoresPreserveInput(t *testing.T) {
	summary := summaryFor(20, 30, metric(0.01), 50e6, 10)
	before := summary
	_ = networkQualityScores(summary)
	if summary != before {
		t.Fatalf("networkQualityScores modified input: before %#v, after %#v", before, summary)
	}
}

func metric(value float64) optionalMetric {
	return optionalMetric{Value: value, Valid: true}
}

func summaryFor(latencyMs, loadedIncreaseMs float64, packetLoss optionalMetric, downloadBps, jitterMs float64) qualitySummary {
	loadedLatency := latencyMs + loadedIncreaseMs
	return qualitySummary{
		DownloadBps:         metric(downloadBps),
		LatencyMs:           metric(latencyMs),
		JitterMs:            metric(jitterMs),
		DownLoadedLatencyMs: metric(loadedLatency),
		UpLoadedLatencyMs:   metric(loadedLatency),
		PacketLoss:          packetLoss,
	}
}

func scoreByName(scores []qualityScore, name string) qualityScore {
	for _, score := range scores {
		if score.Name == name {
			return score
		}
	}
	return qualityScore{}
}

func setMetric(summary qualitySummary, name string, value optionalMetric) qualitySummary {
	switch name {
	case "download":
		summary.DownloadBps = value
	case "jitter":
		summary.JitterMs = value
	case "download-loaded":
		summary.DownLoadedLatencyMs = value
	case "upload-loaded":
		summary.UpLoadedLatencyMs = value
	case "latency":
		summary.LatencyMs = value
	case "loss":
		summary.PacketLoss = value
	}
	return summary
}

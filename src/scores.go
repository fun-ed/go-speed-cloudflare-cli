package main

import "math"

type optionalMetric struct {
	Value float64
	Valid bool
}

type qualitySummary struct {
	DownloadBps         optionalMetric
	UploadBps           optionalMetric
	LatencyMs           optionalMetric
	JitterMs            optionalMetric
	DownLoadedLatencyMs optionalMetric
	UpLoadedLatencyMs   optionalMetric
	PacketLoss          optionalMetric
}

type qualityScore struct {
	Name           string
	Points         int
	Classification string
	Available      bool
}

type metricScale struct {
	thresholds []float64
	points     []int
}

var (
	packetLossScale = metricScale{
		thresholds: []float64{0.01, 0.05, 0.25, 0.5},
		points:     []int{10, 5, 0, -10, -20},
	}
	latencyScale = metricScale{
		thresholds: []float64{10, 20, 50, 100, 500},
		points:     []int{20, 10, 5, 0, -10, -20},
	}
	jitterScale = metricScale{
		thresholds: []float64{10, 20, 100, 500},
		points:     []int{10, 5, 0, -10, -20},
	}
	downloadScale = metricScale{
		thresholds: []float64{1e6, 10e6, 50e6, 100e6},
		points:     []int{0, 5, 10, 20, 30},
	}
	// Upload has an upstream point scale, but none of the three default
	// experiences uses upload throughput.
	uploadScale = metricScale{
		thresholds: []float64{1e6, 10e6, 50e6, 100e6},
		points:     []int{0, 5, 10, 20, 30},
	}
)

var classifications = []string{"bad", "poor", "average", "good", "great"}

// networkQualityScores calculates Cloudflare 1.14.1's default streaming,
// gaming, and RTC scores. A score is available only when all of its required
// non-loss metrics are valid. Missing or invalid packet loss follows the
// upstream neutral-zero-points rule; it is not treated as measured zero loss.
func networkQualityScores(summary qualitySummary) []qualityScore {
	latencyOK := finiteMetric(summary.LatencyMs)
	loadedOK := latencyOK &&
		finiteMetric(summary.DownLoadedLatencyMs) &&
		finiteMetric(summary.UpLoadedLatencyMs)
	lossPoints := 0
	if validPacketLoss(summary.PacketLoss) {
		lossPoints = metricPoints(summary.PacketLoss.Value, packetLossScale)
	}

	latencyPoints := 0
	if latencyOK {
		latencyPoints = metricPoints(summary.LatencyMs.Value, latencyScale)
	}

	loadedPoints := 0
	if loadedOK {
		loadedIncrease := math.Max(summary.DownLoadedLatencyMs.Value, summary.UpLoadedLatencyMs.Value) - summary.LatencyMs.Value
		loadedPoints = metricPoints(loadedIncrease, latencyScale)
	}

	downloadOK := finitePositiveMetric(summary.DownloadBps)
	jitterOK := finiteMetric(summary.JitterMs)

	streamingPoints := 0
	if latencyOK && loadedOK && downloadOK {
		streamingPoints = latencyPoints + lossPoints + loadedPoints + metricPoints(summary.DownloadBps.Value, downloadScale)
	}
	gamingPoints := 0
	if latencyOK && loadedOK {
		gamingPoints = latencyPoints + lossPoints + loadedPoints
	}
	rtcPoints := 0
	if latencyOK && loadedOK && jitterOK {
		rtcPoints = latencyPoints + lossPoints + loadedPoints + metricPoints(summary.JitterMs.Value, jitterScale)
	}

	return []qualityScore{
		makeQualityScore("streaming", streamingPoints, latencyOK && loadedOK && downloadOK, []int{15, 20, 40, 60}),
		makeQualityScore("gaming", gamingPoints, latencyOK && loadedOK, []int{5, 15, 25, 30}),
		makeQualityScore("rtc", rtcPoints, latencyOK && loadedOK && jitterOK, []int{5, 15, 25, 40}),
	}
}

func finiteMetric(metric optionalMetric) bool {
	return metric.Valid && finiteNonnegative(metric.Value)
}

func finitePositiveMetric(metric optionalMetric) bool {
	return metric.Valid && finitePositive(metric.Value)
}

func validPacketLoss(metric optionalMetric) bool {
	return finiteMetric(metric) && metric.Value <= 1
}

// metricPoints applies a threshold table where equality advances to the next
// point bucket, matching Cloudflare's scaleThreshold behavior.
func metricPoints(value float64, scale metricScale) int {
	for index, threshold := range scale.thresholds {
		if value < threshold {
			return scale.points[index]
		}
	}
	return scale.points[len(scale.points)-1]
}

func makeQualityScore(name string, points int, available bool, thresholds []int) qualityScore {
	if !available {
		return qualityScore{Name: name, Classification: "unavailable"}
	}
	if points < 0 {
		points = 0
	}

	classificationIndex := 0
	for _, threshold := range thresholds {
		if points < threshold {
			break
		}
		classificationIndex++
	}
	return qualityScore{
		Name:           name,
		Points:         points,
		Classification: classifications[classificationIndex],
		Available:      true,
	}
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func parseMetricsFile(path string) (map[string]*dto.MetricFamily, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	parser := expfmt.NewTextParser(model.LegacyValidation)
	return parser.TextToMetricFamilies(file)
}

func metricsGauge(t *testing.T, families map[string]*dto.MetricFamily, name string, labels prometheus.Labels) (float64, bool) {
	t.Helper()
	family := families["cloudflare_speedtest_"+name]
	if family == nil {
		return 0, false
	}
	for _, metric := range family.Metric {
		if len(metric.Label) != len(labels) {
			continue
		}
		matches := true
		for _, label := range metric.Label {
			if value, exists := labels[label.GetName()]; !exists || value != label.GetValue() {
				matches = false
			}
		}
		if matches {
			if metric.Gauge == nil || family.GetType() != dto.MetricType_GAUGE {
				t.Fatalf("%s is not a gauge", name)
			}
			return metric.Gauge.GetValue(), true
		}
	}
	return 0, false
}

func wantMetricsGauge(t *testing.T, families map[string]*dto.MetricFamily, name string, labels prometheus.Labels, want float64) {
	t.Helper()
	got, ok := metricsGauge(t, families, name, labels)
	if !ok || math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s%v=%g available=%v; want %g", name, labels, got, ok, want)
	}
}

func metricsFixture() metricsSnapshot {
	return metricsSnapshot{
		Download: true, Upload: true, LoadedLatency: true, PacketLoss: true,
		Summary: qualitySummary{
			DownloadBps: optionalMetric{Value: 42.5e6, Valid: true}, UploadBps: optionalMetric{Value: 12.3e6, Valid: true},
			LatencyMs: optionalMetric{Value: 46, Valid: true}, JitterMs: optionalMetric{Value: 43.1, Valid: true},
			DownLoadedLatencyMs: optionalMetric{Value: 99, Valid: true}, UpLoadedLatencyMs: optionalMetric{Value: 88, Valid: true},
			PacketLoss: optionalMetric{Value: 0, Valid: true},
		},
		DownloadLoadedJitterMs: optionalMetric{Value: 53.7, Valid: true}, UploadLoadedJitterMs: optionalMetric{Value: 115, Valid: true},
		CompletedAt: time.Unix(1800000000, 0),
	}
}

func TestMetricsFileUsesPrometheusUnitsAndBoundedLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	snapshot := metricsFixture()
	if err := writeMetricsFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "build_info", prometheus.Labels{"version": version}, 1)
	wantMetricsGauge(t, families, "run_success", nil, 1)
	wantMetricsGauge(t, families, "run_completed_timestamp_seconds", nil, 1800000000)
	wantMetricsGauge(t, families, "bandwidth_bits_per_second", prometheus.Labels{"direction": "download"}, 42.5e6)
	wantMetricsGauge(t, families, "bandwidth_bits_per_second", prometheus.Labels{"direction": "upload"}, 12.3e6)
	wantMetricsGauge(t, families, "latency_seconds", prometheus.Labels{"load": "unloaded"}, 0.046)
	wantMetricsGauge(t, families, "latency_seconds", prometheus.Labels{"load": "download"}, 0.099)
	wantMetricsGauge(t, families, "latency_seconds", prometheus.Labels{"load": "upload"}, 0.088)
	wantMetricsGauge(t, families, "jitter_seconds", prometheus.Labels{"load": "unloaded"}, 0.0431)
	wantMetricsGauge(t, families, "jitter_seconds", prometheus.Labels{"load": "download"}, 0.0537)
	wantMetricsGauge(t, families, "jitter_seconds", prometheus.Labels{"load": "upload"}, 0.115)
	wantMetricsGauge(t, families, "packet_loss_ratio", nil, 0)
	wantMetricsGauge(t, families, "packet_loss_available", nil, 1)
	for _, score := range networkQualityScores(snapshot.Summary) {
		wantMetricsGauge(t, families, "network_quality_points", prometheus.Labels{"category": score.Name}, float64(score.Points))
	}
	for name, family := range families {
		if family.GetHelp() == "" || !strings.HasPrefix(name, "cloudflare_speedtest_") {
			t.Fatalf("unexpected metric family: %s", name)
		}
		for _, metric := range family.Metric {
			if metric.TimestampMs != nil {
				t.Fatalf("%s has a sample timestamp unsupported by the textfile collector", name)
			}
			for _, label := range metric.Label {
				switch label.GetName() {
				case "version", "direction", "load", "category":
				default:
					t.Fatalf("unexpected or identifying label: %s", label.GetName())
				}
			}
		}
	}
}

func TestMetricsUnavailableAndDisabledAreNotMeasuredZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	snapshot := metricsSnapshot{Download: true, PacketLoss: true, ExitCode: 130, CompletedAt: time.Now(), Summary: qualitySummary{
		DownloadBps: optionalMetric{Value: math.Inf(1), Valid: true},
		LatencyMs:   optionalMetric{Value: math.NaN(), Valid: true},
	}}
	if err := writeMetricsFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "run_success", nil, 0)
	wantMetricsGauge(t, families, "run_exit_code", nil, 130)
	wantMetricsGauge(t, families, "bandwidth_available", prometheus.Labels{"direction": "download"}, 0)
	wantMetricsGauge(t, families, "packet_loss_available", nil, 0)
	for _, name := range []string{"bandwidth_bits_per_second", "packet_loss_ratio", "network_quality_points"} {
		if families["cloudflare_speedtest_"+name] != nil {
			t.Fatalf("unavailable value emitted as measured data: %s", name)
		}
	}
	if _, ok := metricsGauge(t, families, "bandwidth_available", prometheus.Labels{"direction": "upload"}); ok {
		t.Fatal("disabled upload was exported")
	}
	if _, ok := metricsGauge(t, families, "latency_available", prometheus.Labels{"load": "download"}); ok {
		t.Fatal("disabled loaded latency was exported")
	}
}

func TestMetricsPreserveAvailableZeroQualityPoints(t *testing.T) {
	snapshot := metricsFixture()
	snapshot.Summary.LatencyMs.Value = 1000
	snapshot.Summary.JitterMs.Value = 1000
	snapshot.Summary.DownLoadedLatencyMs.Value = 2000
	snapshot.Summary.UpLoadedLatencyMs.Value = 2000
	snapshot.Summary.DownloadBps.Value, snapshot.Summary.UploadBps.Value = 1000, 1000
	snapshot.Summary.PacketLoss.Value = 0.9
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	if err := writeMetricsFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, score := range networkQualityScores(snapshot.Summary) {
		if !score.Available || score.Points != 0 {
			t.Fatal("fixture did not exercise available zero NQS points")
		}
		wantMetricsGauge(t, families, "network_quality_points", prometheus.Labels{"category": score.Name}, 0)
		wantMetricsGauge(t, families, "network_quality_available", prometheus.Labels{"category": score.Name}, 1)
	}
}

func TestMetricsFileReplacesSnapshotAndPreservesPermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "speedtest.prom")
	first := metricsFixture()
	if err := writeMetricsFile(path, first); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("new metrics file is not private")
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	second := metricsSnapshot{Download: true, ExitCode: 1, CompletedAt: time.Unix(1800000001, 0)}
	if err := writeMetricsFile(path, second); err != nil {
		t.Fatal(err)
	}
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "run_exit_code", nil, 1)
	if families["cloudflare_speedtest_packet_loss_ratio"] != nil {
		t.Fatal("old measurement survived replacement")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatal("existing collector read permissions were not preserved")
		}
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary metrics files leaked: %v %v", files, err)
	}
}

func TestCLIMetricsSuccessFailureAndCancellation(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		failure  bool
		canceled bool
		code     int
	}{{name: "success"}, {name: "failure", failure: true, code: 1}, {name: "canceled", canceled: true, code: 130}} {
		t.Run(scenario.name, func(t *testing.T) {
			server, _, _ := cliFixture(t, scenario.failure, false)
			path := filepath.Join(t.TempDir(), "speedtest.prom")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario.canceled {
				cancel()
			}
			var stdout, stderr bytes.Buffer
			code := runCLIContext(ctx, []string{"-lite", "-loaded-latency=false", "-packet-loss=false", "-metrics-file", path}, &stdout, &stderr,
				cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
			if code != scenario.code {
				t.Fatalf("exit=%d, want=%d; %s", code, scenario.code, stderr.String())
			}
			families, err := parseMetricsFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wantMetricsGauge(t, families, "run_exit_code", nil, float64(code))
			if code == 0 {
				wantMetricsGauge(t, families, "run_success", nil, 1)
				value, ok := metricsGauge(t, families, "bandwidth_bits_per_second", prometheus.Labels{"direction": "download"})
				if !ok || !strings.Contains(stdout.String(), fmt.Sprintf("Download speed (P90): %.2f Mbps", value/1e6)) {
					t.Fatal("text and metrics bandwidth disagree")
				}
			} else {
				wantMetricsGauge(t, families, "run_success", nil, 0)
				wantMetricsGauge(t, families, "bandwidth_available", prometheus.Labels{"direction": "download"}, 0)
			}
			if strings.Contains(stdout.String(), "# HELP") || strings.Contains(stderr.String(), "# HELP") {
				t.Fatal("metrics polluted CLI output")
			}
		})
	}
}

func TestCLIMetricsInvalidPathFailsBeforeNetworkAndVersionStaysOffline(t *testing.T) {
	server, down, up := cliFixture(t, false, false)
	root := t.TempDir()
	badDirectory := filepath.Join(root, "directory.prom")
	if err := os.Mkdir(badDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "not-prom.txt"), filepath.Join(root, "missing", "speedtest.prom"), badDirectory} {
		var stdout, stderr bytes.Buffer
		code := runCLIContext(context.Background(), []string{"-metrics-file", path}, &stdout, &stderr,
			cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
		if code != 2 || stdout.Len() != 0 || down.Load() != 0 || up.Load() != 0 {
			t.Fatalf("invalid output path started measurements: exit=%d, %s", code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runCLIContext(context.Background(), []string{"-version", "-metrics-file", "bad.txt"}, &stdout, &stderr, cliConfig{}); code != 0 || !strings.Contains(stdout.String(), "version "+version) || stderr.Len() != 0 {
		t.Fatal("version command did not stay offline")
	}
}

func TestMetricsFileRejectsSymlinkWithoutTouchingDestination(t *testing.T) {
	directory := t.TempDir()
	original := filepath.Join(directory, "original")
	if err := os.WriteFile(original, []byte("original data"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "speedtest.prom")
	if err := os.Symlink(original, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeMetricsFile(path, metricsFixture()); err == nil {
		t.Fatal("symlink target was accepted")
	}
	data, err := os.ReadFile(original)
	if err != nil || string(data) != "original data" {
		t.Fatal("symlink destination changed")
	}
}

func TestCLIMetricsWriteFailureDoesNotReportSuccess(t *testing.T) {
	server, _, _ := cliFixture(t, false, false)
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	created := false
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-lite", "-loaded-latency=false", "-packet-loss=false", "-metrics-file", path}, &stdout, &stderr,
		cliConfig{baseURL: server.URL, phases: smallCLIPlan(), observe: func(any) {
			if !created {
				created = true
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
		}})
	if code != 1 || strings.Contains(stdout.String(), "Test complete.") || !strings.Contains(stderr.String(), "metrics export failed") {
		t.Fatalf("export failure looks successful: exit=%d, %s %s", code, stdout.String(), stderr.String())
	}
}

func TestTUIMetricsFollowCancellationAndRerun(t *testing.T) {
	server, _, _ := cliFixture(t, false, false)
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	opts := tuiTestOptions(t, "-metrics-file", path)
	m := newTUIModel(context.Background(), lifetime, opts, cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	m.startRun()
	finishTUIRun(t, m)
	m.Update(tea.KeyPressMsg{Code: 'r'})
	m.cancel()
	finishTUIRun(t, m)
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "run_exit_code", nil, 130)
	wantMetricsGauge(t, families, "run_success", nil, 0)
	m.Update(tea.KeyPressMsg{Code: 'r'})
	finishTUIRun(t, m)
	families, err = parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "run_exit_code", nil, 0)
	wantMetricsGauge(t, families, "run_success", nil, 1)
}

func TestMetricsRejectZeroBandwidthAndOutOfRangeLoss(t *testing.T) {
	snapshot := metricsFixture()
	snapshot.Summary.DownloadBps.Value = 0
	snapshot.Summary.PacketLoss.Value = 2
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	if err := writeMetricsFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	families, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantMetricsGauge(t, families, "bandwidth_available", prometheus.Labels{"direction": "download"}, 0)
	wantMetricsGauge(t, families, "packet_loss_available", nil, 0)
	if _, ok := metricsGauge(t, families, "bandwidth_bits_per_second", prometheus.Labels{"direction": "download"}); ok {
		t.Fatal("zero bandwidth was exported as a valid measurement")
	}
	if families["cloudflare_speedtest_packet_loss_ratio"] != nil {
		t.Fatal("out-of-range packet loss was exported")
	}
}

func TestMetricsConcurrentReadersSeeWholeSnapshots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("same-directory atomic rename is a Unix filesystem contract")
	}
	path := filepath.Join(t.TempDir(), "speedtest.prom")
	snapshot := metricsFixture()
	snapshot.Summary.UploadBps.Value = 2 * snapshot.Summary.DownloadBps.Value
	if err := writeMetricsFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	initial, err := parseMetricsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, failures := make(chan struct{}), make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		defer close(done)
		reading := false
		for ctx.Err() == nil {
			families, err := parseMetricsFile(path)
			if err != nil || len(families) != len(initial) {
				failures <- fmt.Errorf("partial metrics snapshot: families=%d error=%v", len(families), err)
				return
			}
			values := make(map[string]float64)
			for _, metric := range families["cloudflare_speedtest_bandwidth_bits_per_second"].GetMetric() {
				for _, label := range metric.Label {
					if label.GetName() == "direction" {
						values[label.GetValue()] = metric.GetGauge().GetValue()
					}
				}
			}
			if len(values) != 2 || values["upload"] != 2*values["download"] {
				failures <- fmt.Errorf("mixed metrics snapshots: %v", values)
				return
			}
			if !reading {
				close(ready)
				reading = true
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-done:
		t.Fatal("metrics reader failed before the first snapshot")
	case <-time.After(3 * time.Second):
		t.Fatal("metrics reader did not start")
	}
	for i := range 20 {
		snapshot.Summary.DownloadBps.Value = float64(i+1) * 1e6
		snapshot.Summary.UploadBps.Value = 2 * snapshot.Summary.DownloadBps.Value
		if err := writeMetricsFile(path, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	<-done
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

type metricsSnapshot struct {
	Summary                                      qualitySummary
	Download, Upload, LoadedLatency, PacketLoss  bool
	DownloadLoadedJitterMs, UploadLoadedJitterMs optionalMetric
	ExitCode                                     int
	CompletedAt                                  time.Time
}

type snapshotCollector struct{ snapshot metricsSnapshot }

func (c snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	emit := func(name, help string, value float64, labels prometheus.Labels) {
		desc := prometheus.NewDesc("cloudflare_speedtest_"+name, help, nil, labels)
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value)
	}
	optional := func(name, unit, help string, metric optionalMetric, scale float64, labels prometheus.Labels) {
		available := 0.0
		value := metric.Value * scale
		if metric.Valid && finiteNonnegative(metric.Value) && finiteNonnegative(value) {
			available = 1
			emit(name+"_"+unit, help, value, labels)
		}
		emit(name+"_available", "Whether the corresponding measurement is available (1 or 0).", available, labels)
	}
	s := c.snapshot
	success := 0.0
	if s.ExitCode == 0 {
		success = 1
	}
	emit("build_info", "CLI build information.", 1, prometheus.Labels{"version": version})
	emit("run_success", "Whether the last measurement run succeeded (1 or 0).", success, nil)
	emit("run_exit_code", "Exit code of the last measurement run: 0, 1, or 130.", float64(s.ExitCode), nil)
	emit("run_completed_timestamp_seconds", "Unix timestamp when the last measurement run finished.", float64(s.CompletedAt.Unix()), nil)
	optional("latency", "seconds", "Median latency in seconds.", s.Summary.LatencyMs, 0.001, prometheus.Labels{"load": "unloaded"})
	optional("jitter", "seconds", "Mean absolute difference of consecutive latency probes in seconds.", s.Summary.JitterMs, 0.001, prometheus.Labels{"load": "unloaded"})
	for _, direction := range []string{measurementDownload, measurementUpload} {
		if direction == measurementDownload && !s.Download || direction == measurementUpload && !s.Upload {
			continue
		}
		speed, latency, jitter := s.Summary.DownloadBps, s.Summary.DownLoadedLatencyMs, s.DownloadLoadedJitterMs
		if direction == measurementUpload {
			speed, latency, jitter = s.Summary.UploadBps, s.Summary.UpLoadedLatencyMs, s.UploadLoadedJitterMs
		}
		speed.Valid = finitePositiveMetric(speed)
		optional("bandwidth", "bits_per_second", "Pooled P90 bandwidth in bits per second.", speed, 1, prometheus.Labels{"direction": direction})
		if s.LoadedLatency {
			optional("latency", "seconds", "Median latency in seconds.", latency, 0.001, prometheus.Labels{"load": direction})
			optional("jitter", "seconds", "Mean absolute difference of consecutive latency probes in seconds.", jitter, 0.001, prometheus.Labels{"load": direction})
		}
	}
	if s.PacketLoss {
		loss := s.Summary.PacketLoss
		loss.Valid = validPacketLoss(loss)
		optional("packet_loss", "ratio", "Fraction of WebRTC application messages not received, from 0 to 1.", loss, 1, nil)
	}
	for _, score := range networkQualityScores(s.Summary) {
		available := 0.0
		labels := prometheus.Labels{"category": score.Name}
		if score.Available {
			available = 1
			emit("network_quality_points", "Network Quality Score points as reported by the CLI, floored at 0 and not normalized to 100.", float64(score.Points), labels)
		}
		emit("network_quality_available", "Whether the Network Quality Score category is available (1 or 0).", available, labels)
	}
}

func metricsFileMode(path string) (os.FileMode, error) {
	if filepath.Ext(path) != ".prom" {
		return 0, errors.New("metrics file must have a .prom extension")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return 0, fmt.Errorf("metrics parent directory: %w", err)
	}
	if !parent.IsDir() {
		return 0, errors.New("metrics parent must be a directory")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0o600, nil
	}
	if err != nil {
		return 0, fmt.Errorf("metrics target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("metrics target must be a regular file, not a symlink or directory")
	}
	return info.Mode().Perm(), nil
}

func writeMetricsFile(path string, snapshot metricsSnapshot) error {
	mode, err := metricsFileMode(path)
	if err != nil {
		return err
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(snapshotCollector{snapshot: snapshot}); err != nil {
		return fmt.Errorf("register metrics: %w", err)
	}
	families, err := registry.Gather()
	if err != nil {
		return fmt.Errorf("gather metrics: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cloudflare-speedtest-*.tmp")
	if err != nil {
		return fmt.Errorf("create metrics temporary file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = os.Remove(file.Name())
	}()
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set metrics file permissions: %w", err)
	}
	encoder := expfmt.NewEncoder(file, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			return fmt.Errorf("encode metrics: %w", err)
		}
	}
	if closer, ok := encoder.(expfmt.Closer); ok {
		if err := closer.Close(); err != nil {
			return fmt.Errorf("finish metrics encoding: %w", err)
		}
	}
	err = file.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("close metrics file: %w", err)
	}
	// The temporary file shares the destination directory so Unix readers see a complete snapshot.
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace metrics file: %w", err)
	}
	return nil
}

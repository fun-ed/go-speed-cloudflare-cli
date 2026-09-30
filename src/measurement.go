package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type phase struct {
	Direction    string
	Bytes        int
	Count        int
	BypassFinish bool
}

type sample struct {
	Direction  string
	Bytes      int
	DurationMs float64
	LatencyMs  float64
	SpeedBps   float64
	Started    time.Time
}

type phaseResult struct {
	Phase          phase
	Samples        []sample
	LoadedLatency  []sample
	Attempts       int
	Failures       int
	Err            error
	LoadedFailures int
	LoadedErr      error
}

const (
	measurementDownload = "download"
	measurementUpload   = "upload"
	measurementLatency  = "latency"

	defaultMeasurementTimeout = 30 * time.Second
	max429Retries             = 3
	loadedLatencyStartDelay   = 20 * time.Millisecond
	loadedLatencyInterval     = 400 * time.Millisecond
	bandwidthMinDuration      = 10 * time.Millisecond
	loadedMinDuration         = 250 * time.Millisecond
	finishDuration            = 1000 * time.Millisecond
	estimatedHeaderFraction   = 0.005
	serverTimeMinDurationMs   = 0.01
	serverTimeDeltaMaxMs      = 15.0
	serverTimeCalibMaxMs      = 150.0
	serverTimeDeltaWeight     = 0.75
)

var serverTimingMetric = regexp.MustCompile(`(?i)(?:^|,)\s*(cfReq(?:uest)?Dur(?:ation)?|cfSpeed[A-Za-z]*)\s*;\s*dur\s*=\s*([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)`)

type measurementClient struct {
	baseURL string
	timeout time.Duration
	client  *http.Client

	deltaMu         sync.Mutex
	serverTimeDelta float64
}

type traceTiming struct {
	mu sync.Mutex

	requestStart  time.Time
	headerStart   time.Time
	firstByte     time.Time
	connectStarts map[string]time.Time
	lastConnTime  time.Duration
	newConn       bool
}

func newMeasurementClient(baseURL string, timeout time.Duration) *measurementClient {
	if timeout <= 0 {
		timeout = defaultMeasurementTimeout
	}
	var transport http.RoundTripper
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	} else {
		transport = http.DefaultTransport
	}
	return &measurementClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		timeout: timeout,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
	}
}

func (c *measurementClient) measure(ctx context.Context, direction string, size int) (sample, error) {
	return c.measureDuring(ctx, direction, size, "idle")
}

func (c *measurementClient) measureDuring(ctx context.Context, direction string, size int, during string) (sample, error) {
	var result sample
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if size < 0 {
		return result, fmt.Errorf("measurement size must not be negative: %d", size)
	}

	method := http.MethodGet
	path := "__down"
	querySize := size
	switch direction {
	case measurementLatency:
		if size != 0 {
			return result, fmt.Errorf("latency measurement size must be zero")
		}
		querySize = 0
	case measurementDownload:
		if size == 0 {
			return result, fmt.Errorf("download measurement size must be positive")
		}
	case measurementUpload:
		if size == 0 {
			return result, fmt.Errorf("upload measurement size must be positive")
		}
		method = http.MethodPost
		path = "__up"
	default:
		return result, fmt.Errorf("unsupported measurement direction %q", direction)
	}

	target, err := c.measurementURL(path, querySize, direction, during)
	if err != nil {
		return result, err
	}

	started := time.Now()
	var lastErr error
	for retry := range max429Retries + 1 {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		trace := &traceTiming{requestStart: time.Now()}
		req, reqErr := http.NewRequestWithContext(ctx, method, target.String(), nil)
		if reqErr != nil {
			return result, reqErr
		}
		if method == http.MethodPost {
			bodySize := int64(size)
			req.Body = newZeroBody(bodySize)
			req.GetBody = func() (io.ReadCloser, error) { return newZeroBody(bodySize), nil }
			req.ContentLength = bodySize
			req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "*/*")
		if req.URL.Scheme == "http" || req.URL.Scheme == "https" {
			req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace.clientTrace()))

		resp, doErr := c.client.Do(req)
		if doErr != nil {
			return result, fmt.Errorf("%s request failed: %w", direction, doErr)
		}
		serverTime, hasServerTime := parseServerTime(resp.Header.Values("Server-Timing"))
		status := resp.StatusCode
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			drainErr := drainAndClose(resp.Body)
			statusErr := fmt.Errorf("%s request returned HTTP %d", direction, status)
			if drainErr != nil {
				statusErr = errors.Join(statusErr, fmt.Errorf("drain error response body: %w", drainErr))
			}
			if status == http.StatusTooManyRequests && drainErr == nil && retry < max429Retries {
				waitErr := waitRetryAfter(ctx, retryDelay(resp.Header.Get("Retry-After")))
				if waitErr != nil {
					return result, fmt.Errorf("%w; retry wait: %w", statusErr, waitErr)
				}
				lastErr = statusErr
				continue
			}
			if status == http.StatusTooManyRequests && retry == max429Retries {
				return result, fmt.Errorf("%w after %d retries", statusErr, max429Retries)
			}
			return result, statusErr
		}

		bodyStarted := time.Now()
		bodyBytes, drainErr := drainCountAndClose(resp.Body)
		bodyEnded := time.Now()
		if drainErr != nil {
			return result, fmt.Errorf("read %s response body: %w", direction, drainErr)
		}
		if direction == measurementDownload && int64(size) != bodyBytes {
			return result, fmt.Errorf("download response size mismatch: requested %d bytes, received %d", size, bodyBytes)
		}

		ttfb, firstByte := trace.measurementTimes(bodyStarted)
		if !finiteNonnegative(ttfb) {
			return result, fmt.Errorf("invalid response-header timing")
		}
		var bodyDuration time.Duration
		// GotFirstResponseByte marks arrival of the response headers, so use it
		// as the beginning of payload timing even when the server delays the body.
		if !firstByte.IsZero() {
			bodyDuration = bodyEnded.Sub(firstByte)
		} else {
			bodyDuration = bodyEnded.Sub(bodyStarted)
		}
		if bodyDuration < 0 {
			bodyDuration = 0
		}

		baseServerTime := 0.0
		if hasServerTime {
			baseServerTime = serverTime
		}
		delta := c.serverDeltaForSample(baseServerTime, ttfb, resp.ProtoMajor, trace)
		latencyMs := ttfb - baseServerTime - delta
		if latencyMs <= 1 {
			latencyMs = math.Max(0, ttfb-baseServerTime)
		}
		if !finiteNonnegative(latencyMs) {
			return result, fmt.Errorf("invalid corrected latency")
		}

		duration := ttfb
		if direction == measurementDownload {
			duration = latencyMs + float64(bodyDuration)/float64(time.Millisecond)
		}
		if !finiteNonnegative(duration) {
			return result, fmt.Errorf("invalid %s duration", direction)
		}

		result = sample{
			Direction:  direction,
			Bytes:      size,
			DurationMs: duration,
			LatencyMs:  latencyMs,
			Started:    started,
		}
		if direction == measurementDownload || direction == measurementUpload {
			seconds := duration / 1000
			if seconds <= 0 {
				return sample{}, fmt.Errorf("invalid %s duration: %g ms", direction, duration)
			}
			result.SpeedBps = 8 * float64(size) * (1 + estimatedHeaderFraction) / seconds
			if !finitePositive(result.SpeedBps) {
				return sample{}, fmt.Errorf("invalid %s speed", direction)
			}
		}
		return result, nil
	}
	return result, lastErr
}

func (c *measurementClient) measurementURL(endpoint string, size int, direction, during string) (*url.URL, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid measurement base URL %q", c.baseURL)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + endpoint
	query := base.Query()
	query.Set("bytes", strconv.Itoa(size))
	if direction == measurementLatency {
		if during == "" {
			during = "idle"
		}
		query.Set("during", during)
	}
	base.RawQuery = query.Encode()
	return base, nil
}

func (t *traceTiming) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		WroteHeaders: func() {
			t.mu.Lock()
			t.headerStart = time.Now()
			t.firstByte = time.Time{}
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			if t.firstByte.IsZero() {
				t.firstByte = time.Now()
			}
			t.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			t.newConn = !info.Reused
			t.mu.Unlock()
		},
		ConnectStart: func(network, addr string) {
			t.mu.Lock()
			if t.connectStarts == nil {
				t.connectStarts = make(map[string]time.Time)
			}
			t.connectStarts[network+"\x00"+addr] = time.Now()
			t.mu.Unlock()
		},
		ConnectDone: func(network, addr string, err error) {
			t.mu.Lock()
			key := network + "\x00" + addr
			started := t.connectStarts[key]
			delete(t.connectStarts, key)
			if err == nil && !started.IsZero() {
				duration := time.Since(started)
				if duration > t.lastConnTime {
					t.lastConnTime = duration
				}
			}
			t.mu.Unlock()
		},
	}
}

func (t *traceTiming) measurementTimes(fallbackFirstByte time.Time) (float64, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	start := t.headerStart
	if start.IsZero() {
		start = t.requestStart
	}
	firstByte := t.firstByte
	if firstByte.IsZero() {
		firstByte = fallbackFirstByte
	}
	return float64(firstByte.Sub(start)) / float64(time.Millisecond), firstByte
}

func (c *measurementClient) serverDeltaForSample(serverTimeMs, ttfbMs float64, protoMajor int, trace *traceTiming) float64 {
	trace.mu.Lock()
	isNewConn := trace.newConn
	connectMs := float64(trace.lastConnTime) / float64(time.Millisecond)
	trace.mu.Unlock()

	c.deltaMu.Lock()
	defer c.deltaMu.Unlock()
	if isNewConn && protoMajor == 1 && connectMs > 0 && serverTimeMs > serverTimeMinDurationMs && serverTimeMs <= serverTimeCalibMaxMs {
		derivedServerTime := math.Max(0, ttfbMs-connectMs)
		delta := derivedServerTime - serverTimeMs
		if delta > 0 && delta <= serverTimeDeltaMaxMs && delta <= serverTimeMs {
			c.serverTimeDelta = c.serverTimeDelta*(1-serverTimeDeltaWeight) + delta*serverTimeDeltaWeight
		}
	}
	return c.serverTimeDelta
}

func parseServerTime(headers []string) (float64, bool) {
	if len(headers) == 0 {
		return 0, false
	}
	joined := strings.Join(headers, ",")
	matches := serverTimingMetric.FindAllStringSubmatch(joined, -1)
	var speedTotal float64
	for _, match := range matches {
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil || !finiteNonnegative(value) || value <= serverTimeMinDurationMs {
			continue
		}
		name := strings.ToLower(match[1])
		if strings.HasPrefix(name, "cfreq") {
			return value, true
		}
		if strings.HasPrefix(name, "cfspeed") {
			speedTotal += value
			if !finitePositive(speedTotal) {
				return 0, false
			}
		}
	}
	if speedTotal > serverTimeMinDurationMs && finitePositive(speedTotal) {
		return speedTotal, true
	}
	return 0, false
}

func retryDelay(header string) time.Duration {
	const fallback = 5 * time.Second
	if header == "" {
		return fallback
	}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil {
		if seconds < 0 || math.IsNaN(seconds) {
			return 0
		}
		maxSeconds := float64(math.MaxInt64) / float64(time.Second)
		if seconds >= maxSeconds {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(seconds * float64(time.Second))
	}
	when, err := http.ParseTime(strings.TrimSpace(header))
	if err != nil {
		return fallback
	}
	delay := time.Until(when)
	if delay < 0 {
		return 0
	}
	return delay
}

func waitRetryAfter(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func drainCountAndClose(body io.ReadCloser) (int64, error) {
	if body == nil {
		return 0, nil
	}
	count, readErr := io.Copy(io.Discard, body)
	closeErr := body.Close()
	return count, errors.Join(readErr, closeErr)
}

func drainAndClose(body io.ReadCloser) error {
	_, err := drainCountAndClose(body)
	return err
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = '0'
	}
	return len(buffer), nil
}

func newZeroBody(size int64) io.ReadCloser {
	return io.NopCloser(io.LimitReader(zeroReader{}, size))
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func finitePositive(value float64) bool {
	return finiteNonnegative(value) && value > 0
}

func defaultPhases() []phase {
	return []phase{
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 100_000, Count: 1, BypassFinish: true},
		{Direction: measurementLatency, Count: 20},
		{Direction: measurementDownload, Bytes: 100_000, Count: 9},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 1_000_000, Count: 8},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementUpload, Bytes: 100_000, Count: 8},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementUpload, Bytes: 1_000_000, Count: 6},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 10_000_000, Count: 6},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementUpload, Bytes: 10_000_000, Count: 4},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 25_000_000, Count: 4},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementUpload, Bytes: 25_000_000, Count: 4},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 100_000_000, Count: 3},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementUpload, Bytes: 50_000_000, Count: 3},
		{Direction: measurementLatency, Count: 2},
		{Direction: measurementDownload, Bytes: 250_000_000, Count: 2},
	}
}

func (c *measurementClient) runPhase(ctx context.Context, p phase, loaded bool, onAttempt func(int, *sample, error)) phaseResult {
	result := phaseResult{Phase: p}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.Count < 0 || (p.Direction != measurementLatency && p.Direction != measurementDownload && p.Direction != measurementUpload) || (p.Direction == measurementLatency && p.Bytes != 0) || (p.Direction != measurementLatency && p.Bytes <= 0) {
		result.Err = fmt.Errorf("invalid measurement phase: direction=%q bytes=%d count=%d", p.Direction, p.Bytes, p.Count)
		return result
	}

	phaseCtx, cancel := context.WithCancel(ctx)
	var normalCompletion atomic.Bool
	var loadedDone chan struct{}
	if loaded && p.Direction != measurementLatency {
		loadedDone = make(chan struct{})
		go func() {
			defer close(loadedDone)
			if !waitContext(phaseCtx, loadedLatencyStartDelay) {
				return
			}
			for {
				latency, err := c.measureDuring(phaseCtx, measurementLatency, 0, p.Direction)
				if err == nil {
					result.LoadedLatency = append(result.LoadedLatency, latency)
				} else if !normalCompletion.Load() || !errors.Is(err, context.Canceled) {
					result.LoadedFailures++
					result.LoadedErr = errors.Join(result.LoadedErr, err)
				}
				if phaseCtx.Err() != nil || !waitContext(phaseCtx, loadedLatencyInterval) {
					return
				}
			}
		}()
	}

	for range p.Count {
		if err := phaseCtx.Err(); err != nil {
			result.Err = errors.Join(result.Err, err)
			break
		}
		result.Attempts++
		measured, err := c.measure(phaseCtx, p.Direction, p.Bytes)
		if err != nil {
			result.Failures++
			result.Err = errors.Join(result.Err, err)
			if onAttempt != nil {
				onAttempt(result.Attempts, nil, err)
			}
			if phaseCtx.Err() != nil {
				break
			}
			continue
		}
		result.Samples = append(result.Samples, measured)
		if onAttempt != nil {
			attemptSample := measured
			onAttempt(result.Attempts, &attemptSample, nil)
		}
	}

	if ctx.Err() == nil {
		normalCompletion.Store(true)
	}
	cancel()
	if loadedDone != nil {
		<-loadedDone
	}
	return result
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func shouldFinish(result phaseResult) bool {
	if result.Phase.BypassFinish || len(result.Samples) == 0 {
		return false
	}
	minimum := math.Inf(1)
	for _, measured := range result.Samples {
		if finiteNonnegative(measured.DurationMs) && measured.DurationMs < minimum {
			minimum = measured.DurationMs
		}
	}
	return minimum > float64(finishDuration)/float64(time.Millisecond)
}

func bandwidthBps(samples []sample) (float64, bool) {
	speeds := make([]float64, 0, len(samples))
	for _, measured := range samples {
		if finitePositive(measured.SpeedBps) && finiteNonnegative(measured.DurationMs) && measured.DurationMs >= float64(bandwidthMinDuration)/float64(time.Millisecond) {
			speeds = append(speeds, measured.SpeedBps)
		}
	}
	if len(speeds) == 0 {
		return 0, false
	}
	value := quartile(speeds, 0.9)
	if !finitePositive(value) {
		return 0, false
	}
	return value, true
}

func latencyStats(samples []sample) (latencyMs, jitterMs float64, latencyOK, jitterOK bool) {
	points := make([]float64, 0, len(samples))
	for _, measured := range samples {
		if finiteNonnegative(measured.LatencyMs) {
			points = append(points, measured.LatencyMs)
		}
	}
	if len(points) == 0 {
		return 0, 0, false, false
	}
	latencyMs = quartile(points, 0.5)
	latencyOK = finiteNonnegative(latencyMs)
	if len(points) > 1 {
		jitterMs = jitter(points)
		jitterOK = finiteNonnegative(jitterMs)
	}
	return
}

func loadedLatencyStats(results []phaseResult, direction string) (latencyMs, jitterMs float64, latencyOK, jitterOK bool) {
	type bucket struct {
		samples []sample
		minimum float64
	}
	buckets := make(map[int]*bucket)
	for _, result := range results {
		if result.Phase.Direction != direction || result.Phase.Direction == measurementLatency {
			continue
		}
		b := buckets[result.Phase.Bytes]
		if b == nil {
			b = &bucket{minimum: math.Inf(1)}
			buckets[result.Phase.Bytes] = b
		}
		for _, measured := range result.Samples {
			if !finiteNonnegative(measured.DurationMs) {
				b.minimum = math.Inf(-1)
				continue
			}
			if measured.DurationMs < b.minimum {
				b.minimum = measured.DurationMs
			}
			b.samples = append(b.samples, measured)
		}
	}

	eligible := make(map[int]bool, len(buckets))
	for bytes, b := range buckets {
		eligible[bytes] = len(b.samples) > 0 && b.minimum >= float64(loadedMinDuration)/float64(time.Millisecond)
	}
	points := make([]sample, 0)
	for _, result := range results {
		if result.Phase.Direction != direction || result.Phase.Direction == measurementLatency || !eligible[result.Phase.Bytes] {
			continue
		}
		for _, point := range result.LoadedLatency {
			if finiteNonnegative(point.LatencyMs) {
				points = append(points, point)
			}
		}
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Started.IsZero() {
			return false
		}
		if points[j].Started.IsZero() {
			return true
		}
		return points[i].Started.Before(points[j].Started)
	})
	if len(points) > 20 {
		points = points[len(points)-20:]
	}
	return latencyStats(points)
}

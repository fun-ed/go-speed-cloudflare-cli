package main

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasureDownloadRequestAndExactBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/__down" || r.URL.Query().Get("bytes") != "4" || len(r.URL.Query()) != 1 {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("User-Agent") != userAgent || r.Header.Get("Accept") != "*/*" || r.Header.Get("Origin") != serverOrigin(r) {
			t.Errorf("unexpected fetch headers: %#v", r.Header)
		}
		_, _ = io.WriteString(w, "data")
	}))
	defer server.Close()

	measured, err := newMeasurementClient(server.URL, time.Second).measure(context.Background(), measurementDownload, 4)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Direction != measurementDownload || measured.Bytes != 4 || measured.DurationMs <= 0 || measured.SpeedBps <= 0 || measured.Started.IsZero() {
		t.Fatalf("unexpected sample: %+v", measured)
	}
	if measured.LatencyMs > measured.DurationMs {
		t.Fatalf("download duration %gms is below corrected latency %gms", measured.DurationMs, measured.LatencyMs)
	}
}

func serverOrigin(r *http.Request) string {
	return "http://" + r.Host
}

func TestLatencyUsesCloudflareControlQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/__down" || query.Get("bytes") != "0" || len(query) != 2 {
			t.Errorf("unexpected latency request: %s %s", r.Method, r.URL.RequestURI())
		}
		switch query.Get("during") {
		case "idle", "download", "upload":
		default:
			t.Errorf("unexpected latency load state %q", query.Get("during"))
		}
		_, _ = io.WriteString(w, "control response")
	}))
	defer server.Close()

	client := newMeasurementClient(server.URL, time.Second)
	for _, during := range []string{"idle", "download", "upload"} {
		var measured sample
		var err error
		if during == "idle" {
			measured, err = client.measure(context.Background(), measurementLatency, 0)
		} else {
			measured, err = client.measureDuring(context.Background(), measurementLatency, 0, during)
		}
		if err != nil || measured.Direction != measurementLatency || measured.Bytes != 0 {
			t.Fatalf("latency request during %s failed: sample=%+v err=%v", during, measured, err)
		}
	}
}

func TestTraceTTFBStartsAtWrittenHeaders(t *testing.T) {
	trace := &traceTiming{requestStart: time.Now().Add(-time.Minute)}
	hooks := trace.clientTrace()
	hooks.WroteHeaders()
	time.Sleep(2 * time.Millisecond)
	hooks.GotFirstResponseByte()

	ttfb, _ := trace.measurementTimes(time.Now())
	if ttfb <= 0 || ttfb >= 1000 {
		t.Fatalf("TTFB %gms should start at WroteHeaders, not include connection setup", ttfb)
	}
}

func TestMeasureRejectsHTTPStatusAndMismatchedDownloads(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "non-2xx", status: http.StatusServiceUnavailable, body: "offline"},
		{name: "truncated", status: http.StatusOK, body: "abc"},
		{name: "oversized", status: http.StatusOK, body: "abcde"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			if _, err := newMeasurementClient(server.URL, time.Second).measure(context.Background(), measurementDownload, 4); err == nil {
				t.Fatal("expected request failure")
			}
		})
	}
}

func TestMeasureRetries429WithFreshUploadBodyAndAcceptsEmptySuccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upload body: %v", err)
		}
		if string(body) != "0000" || r.Method != http.MethodPost || r.URL.Path != "/__up" || r.URL.Query().Get("bytes") != "4" || len(r.URL.Query()) != 1 {
			t.Errorf("upload attempt did not have a fresh exact body: %q, %s", body, r.URL.RequestURI())
		}
		if r.Header.Get("Content-Type") != "text/plain;charset=UTF-8" || r.Header.Get("Origin") != serverOrigin(r) || r.Header.Get("sec-gpc") != "" {
			t.Errorf("unexpected upload headers: %#v", r.Header)
		}
		if got := requests.Add(1); got == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	measured, err := newMeasurementClient(server.URL, time.Second).measure(context.Background(), measurementUpload, 4)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || measured.Direction != measurementUpload || measured.SpeedBps <= 0 {
		t.Fatalf("expected one retry and a positive upload sample, requests=%d sample=%+v", requests.Load(), measured)
	}
}

func TestMeasure429RetriesAreBoundedByContext(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := newMeasurementClient(server.URL, time.Second).measure(ctx, measurementLatency, 0)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("expected deadline while waiting after one 429, requests=%d err=%v", requests.Load(), err)
	}
}

func TestMeasureRetryWaitIsBoundedByClientTimeout(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0.5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	started := time.Now()
	_, err := newMeasurementClient(server.URL, 40*time.Millisecond).measure(context.Background(), measurementLatency, 0)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 || elapsed >= 250*time.Millisecond {
		t.Fatalf("retry wait exceeded per-measurement timeout: elapsed=%s requests=%d err=%v", elapsed, requests.Load(), err)
	}
}

func TestMeasureStopsAfterThree429Retries(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := newMeasurementClient(server.URL, time.Second).measure(context.Background(), measurementLatency, 0)
	if err == nil || requests.Load() != max429Retries+1 {
		t.Fatalf("expected one initial attempt and three retries, requests=%d err=%v", requests.Load(), err)
	}
}

func TestMeasureTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	if _, err := newMeasurementClient(server.URL, 15*time.Millisecond).measure(context.Background(), measurementLatency, 0); err == nil {
		t.Fatal("expected request timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newMeasurementClient(server.URL, time.Second).measure(ctx, measurementLatency, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestServerTimingParserPrefersRequestDurationAndSumsSpeedMetrics(t *testing.T) {
	if got, ok := parseServerTime([]string{"cfSpeedA;dur=0.4, cfSpeedB;dur=0.3, cfRequestDuration;dur=4.2"}); !ok || got != 4.2 {
		t.Fatalf("request duration preference: got %g, ok=%v", got, ok)
	}
	if got, ok := parseServerTime([]string{"cfSpeedA;dur=0.02, cfSpeedB;dur=0.03, ignored;dur=7"}); !ok || math.Abs(got-0.05) > 1e-12 {
		t.Fatalf("speed duration sum: got %g, ok=%v", got, ok)
	}
	if got, ok := parseServerTime([]string{"cfReqDur;dur=NaN, cfSpeedA;dur=-1, cfSpeedB;dur=0.01"}); ok || got != 0 {
		t.Fatalf("invalid server timing should be ignored, got %g, ok=%v", got, ok)
	}
}

func TestServerTimeCalibrationOnlyUsesNewHTTP1Connections(t *testing.T) {
	client := newMeasurementClient("http://example.test", time.Second)
	tcpConnectTime := 10 * time.Millisecond // Simulated 80ms TLS handshake is excluded from official TCP calibration.
	trace := &traceTiming{newConn: true, lastConnTime: tcpConnectTime}
	if trace.clientTrace().TLSHandshakeDone != nil {
		t.Fatal("TLS handshake timing must not replace the TCP connect duration")
	}
	if got := client.serverDeltaForSample(80, 100, 1, trace); math.Abs(got-7.5) > 1e-9 {
		t.Fatalf("calibration delta = %g, expected 7.5", got)
	}
	trace.newConn = false
	if got := client.serverDeltaForSample(80, 100, 1, trace); got != 7.5 {
		t.Fatalf("reused connection changed calibration to %g", got)
	}
	trace.newConn = true
	if got := client.serverDeltaForSample(80, 100, 2, trace); got != 7.5 {
		t.Fatalf("HTTP/2 connection changed calibration to %g", got)
	}
}

func TestUploadDurationStopsAtResponseHeadersNotBodyDrain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "response body")
	}))
	defer server.Close()

	measured, err := newMeasurementClient(server.URL, 2*time.Second).measure(context.Background(), measurementUpload, 32)
	if err != nil {
		t.Fatal(err)
	}
	if measured.DurationMs >= 100 {
		t.Fatalf("upload duration includes response-body drain: %gms", measured.DurationMs)
	}
}

func TestDownloadDurationIncludesResponseBodyTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	measured, err := newMeasurementClient(server.URL, time.Second).measure(context.Background(), measurementDownload, 1)
	if err != nil {
		t.Fatal(err)
	}
	if measured.DurationMs < 40 {
		t.Fatalf("download duration omitted delayed body transfer: %gms", measured.DurationMs)
	}
}

func TestMeasureChecksResponseDrainError(t *testing.T) {
	client := newMeasurementClient("http://example.test", time.Second)
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(req.Context())
		trace.WroteHeaders()
		trace.GotFirstResponseByte()
		return &http.Response{
			StatusCode: http.StatusOK,
			ProtoMajor: 1,
			Header:     make(http.Header),
			Body:       failingReadCloser{},
			Request:    req,
		}, nil
	})
	if _, err := client.measure(context.Background(), measurementLatency, 0); err == nil {
		t.Fatal("expected response-body read error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReadCloser) Close() error             { return nil }

func TestRunPhaseLoadedProbeCancellationAndCallbacks(t *testing.T) {
	var mainRequests, sideRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("during") != "" {
			if r.URL.Query().Get("during") != measurementDownload {
				t.Errorf("unexpected loaded probe mode %q", r.URL.Query().Get("during"))
			}
			sideRequests.Add(1)
			_, _ = io.WriteString(w, "control")
			return
		}
		mainRequests.Add(1)
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	client := newMeasurementClient(server.URL, time.Second)
	var callbackCalls atomic.Int32
	result := client.runPhase(context.Background(), phase{Direction: measurementDownload, Bytes: 1, Count: 2}, true, func(attempt int, got *sample, err error) {
		callbackCalls.Add(1)
		if attempt < 1 || err != nil || got == nil || got.Direction != measurementDownload {
			t.Errorf("unexpected main attempt callback: attempt=%d sample=%+v err=%v", attempt, got, err)
		}
	})
	if result.Attempts != 2 || result.Failures != 0 || len(result.Samples) != 2 || callbackCalls.Load() != 2 {
		t.Fatalf("unexpected phase result: %+v callbacks=%d", result, callbackCalls.Load())
	}
	if sideRequests.Load() == 0 || len(result.LoadedLatency) == 0 || mainRequests.Load() != 2 || result.LoadedFailures != 0 || result.LoadedErr != nil {
		t.Fatalf("loaded side probe did not run/join cleanly: side=%d loaded=%d main=%d loaded failures=%d loaded err=%v", sideRequests.Load(), len(result.LoadedLatency), mainRequests.Load(), result.LoadedFailures, result.LoadedErr)
	}
}

func TestRunPhaseParentCancellationJoinsLoadedProbe(t *testing.T) {
	sideStarted := make(chan struct{}, 1)
	var sideRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("during") == measurementDownload {
			sideRequests.Add(1)
			select {
			case sideStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultDone := make(chan phaseResult, 1)
	client := newMeasurementClient(server.URL, time.Second)
	go func() {
		resultDone <- client.runPhase(ctx, phase{Direction: measurementDownload, Bytes: 1, Count: 1}, true, nil)
	}()
	select {
	case <-sideStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("loaded side probe did not start")
	}
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case result := <-resultDone:
		if result.Attempts != 1 || result.Failures != 1 || result.LoadedFailures != 1 || !errors.Is(result.LoadedErr, context.Canceled) || sideRequests.Load() != 1 {
			t.Fatalf("unexpected canceled phase: %+v side requests=%d", result, sideRequests.Load())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("phase returned without joining its canceled loaded probe")
	}
}

func TestRunPhaseTracksLoadedProbeFailuresSeparately(t *testing.T) {
	var sideRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("during") == measurementDownload {
			sideRequests.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	result := newMeasurementClient(server.URL, time.Second).runPhase(context.Background(), phase{Direction: measurementDownload, Bytes: 1, Count: 1}, true, nil)
	if result.Attempts != 1 || result.Failures != 0 || len(result.Samples) != 1 || result.Err != nil {
		t.Fatalf("loaded failure contaminated main measurement: %+v", result)
	}
	if result.LoadedFailures != 1 || result.LoadedErr == nil || !strings.Contains(result.LoadedErr.Error(), "HTTP 503") || sideRequests.Load() != 1 {
		t.Fatalf("loaded probe failure was not retained: failures=%d err=%v requests=%d", result.LoadedFailures, result.LoadedErr, sideRequests.Load())
	}
}

func TestRunPhaseIgnoresExpectedLoadedProbeCancellationAtNormalEnd(t *testing.T) {
	sideStarted := make(chan struct{}, 1)
	var sideRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("during") == measurementDownload {
			sideRequests.Add(1)
			select {
			case sideStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	result := newMeasurementClient(server.URL, time.Second).runPhase(context.Background(), phase{Direction: measurementDownload, Bytes: 1, Count: 1}, true, nil)
	select {
	case <-sideStarted:
	default:
		t.Fatal("loaded probe did not start during the phase")
	}
	if result.Attempts != 1 || result.Failures != 0 || len(result.Samples) != 1 || result.LoadedFailures != 0 || result.LoadedErr != nil || sideRequests.Load() != 1 {
		t.Fatalf("normal phase shutdown was reported as loaded failure: %+v side requests=%d", result, sideRequests.Load())
	}
}

func TestRunPhaseReportsEachMainFailureOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	var callbackCalls int
	result := newMeasurementClient(server.URL, time.Second).runPhase(context.Background(), phase{Direction: measurementLatency, Count: 3}, false, func(attempt int, got *sample, err error) {
		callbackCalls++
		if attempt != callbackCalls || got != nil || err == nil {
			t.Errorf("unexpected failure callback %d: sample=%+v err=%v", attempt, got, err)
		}
	})
	if result.Attempts != 3 || result.Failures != 3 || len(result.Samples) != 0 || callbackCalls != 3 || result.Err == nil {
		t.Fatalf("unexpected all-failure phase result: %+v callbacks=%d", result, callbackCalls)
	}
}

func TestRunPhaseRetainsSlowSampleAndFinishDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(1050 * time.Millisecond)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	result := newMeasurementClient(server.URL, 2*time.Second).runPhase(context.Background(), phase{Direction: measurementDownload, Bytes: 1, Count: 1}, false, nil)
	if len(result.Samples) != 1 || result.Samples[0].DurationMs <= 1000 || !shouldFinish(result) {
		t.Fatalf("slow success should be retained and finish later phases: %+v", result)
	}
	result.Phase.BypassFinish = true
	if shouldFinish(result) {
		t.Fatal("bypass phase should not trigger finish")
	}
}

func TestDefaultPhasesMatchOfficialHTTPSequence(t *testing.T) {
	phases := defaultPhases()
	var got []string
	for _, p := range phases {
		got = append(got, p.Direction+":"+strconv.Itoa(p.Bytes)+":"+strconv.Itoa(p.Count))
	}
	want := []string{
		"latency:0:2", "download:100000:1", "latency:0:20", "download:100000:9",
		"latency:0:2", "download:1000000:8", "latency:0:2", "upload:100000:8",
		"latency:0:2", "upload:1000000:6", "latency:0:2", "download:10000000:6",
		"latency:0:2", "upload:10000000:4", "latency:0:2", "download:25000000:4",
		"latency:0:2", "upload:25000000:4", "latency:0:2", "download:100000000:3",
		"latency:0:2", "upload:50000000:3", "latency:0:2", "download:250000000:2",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phase plan mismatch:\n got %v\nwant %v", got, want)
	}
	if !phases[1].BypassFinish {
		t.Fatal("official first download estimate must bypass the finish threshold")
	}
}

func TestRetryDelaySupportsSecondsAndHTTPDate(t *testing.T) {
	if got := retryDelay("0.25"); got != 250*time.Millisecond {
		t.Fatalf("seconds retry delay = %s", got)
	}
	date := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	got := retryDelay(date)
	if got < time.Second || got > 2*time.Second {
		t.Fatalf("HTTP-date retry delay = %s", got)
	}
}

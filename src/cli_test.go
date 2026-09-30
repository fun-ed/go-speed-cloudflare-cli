package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLIFlags(t *testing.T) {
	cases := []struct {
		name                       string
		args                       []string
		down, up, liteDown, liteUp bool
		wantError                  bool
	}{
		{name: "default", down: true, up: true},
		{name: "download", args: []string{"-download"}, down: true},
		{name: "upload", args: []string{"-upload"}, up: true},
		{name: "lite download selects download", args: []string{"-lite-download"}, down: true, liteDown: true},
		{name: "lite upload selects upload", args: []string{"-lite-upload"}, up: true, liteUp: true},
		{name: "both lite", args: []string{"-lite-download", "-lite-upload"}, down: true, up: true, liteDown: true, liteUp: true},
		{name: "global lite selected upload", args: []string{"-upload", "-lite"}, up: true, liteUp: true},
		{name: "opposite lite", args: []string{"-download", "-lite-upload"}, wantError: true},
		{name: "empty explicit direction", args: []string{"-download=false"}, wantError: true},
		{name: "both false", args: []string{"-download=false", "-upload=false"}, wantError: true},
		{name: "positional", args: []string{"download", "-lite"}, wantError: true},
		{name: "positional before version", args: []string{"junk", "-version"}, wantError: true},
		{name: "zero timeout", args: []string{"-timeout=0"}, wantError: true},
		{name: "negative timeout", args: []string{"-timeout=-1s"}, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseOptions(tc.args, io.Discard)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected usage error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.download != tc.down || opts.upload != tc.up || opts.download && (opts.lite || opts.liteDownload) != tc.liteDown || opts.upload && (opts.lite || opts.liteUpload) != tc.liteUp {
				t.Fatalf("unexpected selection: %+v", opts)
			}
		})
	}
}

func TestCLIOfflineCommands(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"-h"}, {"-help"}} {
		var stdout, stderr bytes.Buffer
		code := runCLIContext(context.Background(), args, &stdout, &stderr, cliConfig{baseURL: "http://127.0.0.1:1"})
		if code != 0 {
			t.Fatalf("%v: exit=%d, stderr=%s", args, code, stderr.String())
		}
		if args[0] == "-version" && !strings.Contains(stdout.String(), version) {
			t.Fatal("version not printed")
		}
		if args[0] != "-version" && !strings.Contains(stderr.String(), "timeout") {
			t.Fatal("help missing documented flag")
		}
	}
}

func TestSelectedPhases(t *testing.T) {
	plan := []phase{{Direction: "latency", Count: 2}, {Direction: "download", Bytes: 10000000, Count: 1}, {Direction: "download", Bytes: 25000000, Count: 1}, {Direction: "upload", Bytes: 10000000, Count: 1}}
	opts, err := parseOptions([]string{"-lite-download"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	selected := selectedPhases(plan, opts)
	if len(selected) != 2 || selected[1].Direction != "download" || selected[1].Bytes != 10000000 {
		t.Fatalf("unexpected lite plan: %+v", selected)
	}
}

func cliFixture(t *testing.T, failMeasurements, failMetadata bool) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var down, up atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("%s missing shared UA", r.URL.Path)
		}
		switch r.URL.Path {
		case "/locations":
			if failMetadata {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `[{"iata":"LOCAL","city":"Local fixture"}]`)
		case "/cdn-cgi/trace":
			if failMetadata {
				_, _ = io.WriteString(w, "<html>not a trace</html>")
				return
			}
			_, _ = io.WriteString(w, "ip=127.0.0.1\nloc=TEST\ncolo=LOCAL\n")
		case "/__down", "/__up":
			if r.Method == http.MethodPost {
				up.Add(1)
			} else if r.URL.Query().Get("bytes") != "0" {
				down.Add(1)
			}
			if failMeasurements {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "unavailable")
				return
			}
			time.Sleep(15 * time.Millisecond)
			size, err := strconv.Atoi(r.URL.Query().Get("bytes"))
			if err != nil {
				t.Errorf("missing bytes query: %s", r.URL.Path)
				w.WriteHeader(400)
				return
			}
			if r.Method == http.MethodPost {
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil || int(n) != size {
					t.Errorf("upload body mismatch: %d != %d", n, size)
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(size))
			_, _ = io.WriteString(w, strings.Repeat("x", size))
		default:
			t.Errorf("unexpected fixture endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &down, &up
}

func smallCLIPlan() []phase {
	return []phase{{Direction: "latency", Count: 2}, {Direction: "download", Bytes: 1000, Count: 2}, {Direction: "upload", Bytes: 1000, Count: 2}}
}

func TestCLIActualHTTPAndDirection(t *testing.T) {
	server, down, up := cliFixture(t, false, false)
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-lite-download", "-loaded-latency=false"}, &stdout, &stderr, cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if down.Load() != 2 || up.Load() != 0 {
		t.Fatalf("wrong direction: down=%d up=%d", down.Load(), up.Load())
	}
	if !strings.Contains(stdout.String(), "Download speed (P90):") || strings.Contains(stdout.String(), "Upload speed (P90):") {
		t.Fatalf("incorrect final labels: %s", stdout.String())
	}
	if strings.ContainsAny(stdout.String()+stderr.String(), "\r\033") {
		t.Fatal("redirected output contains terminal control codes")
	}
}

func TestCLIAllNetworkFailuresAreNotSuccess(t *testing.T) {
	server, _, _ := cliFixture(t, true, true)
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-download", "-loaded-latency=false"}, &stdout, &stderr, cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	if code != 1 {
		t.Fatalf("all failures exited %d", code)
	}
	if !strings.Contains(stdout.String(), "Unloaded latency: N/A") || !strings.Contains(stdout.String(), "Unloaded jitter: N/A") || !strings.Contains(stdout.String(), "Download speed (P90): N/A") {
		t.Fatalf("false healthy-looking output: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "Error:") || stderr.Len() == 0 {
		t.Fatal("errors should be reported on stderr")
	}
}

func TestCLIMetadataFailureWarnsButDoesNotInventFields(t *testing.T) {
	server, _, _ := cliFixture(t, false, true)
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-download", "-loaded-latency=false"}, &stdout, &stderr, cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	if code != 0 {
		t.Fatalf("metadata-only failure should not invalidate throughput: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Server location: N/A") || !strings.Contains(stdout.String(), "Your IP: N/A") || !strings.Contains(stderr.String(), "metadata unavailable") {
		t.Fatalf("metadata failure not observable: %s %s", stdout.String(), stderr.String())
	}
}

func TestCLICancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := runCLIContext(ctx, []string{"-download"}, &stdout, &stderr, cliConfig{baseURL: "http://127.0.0.1:1", phases: smallCLIPlan()})
	if code != 130 {
		t.Fatalf("cancel should exit130, got%d", code)
	}
}

func TestProgressAccountsForSkippedAndFailedWork(t *testing.T) {
	var output bytes.Buffer
	ui := &textUI{stderr: &output, stdout: io.Discard, progress: true, total: 10}
	ui.advance(3, "completed")
	ui.warning("failed sample")
	ui.advance(7, "skipped remaining planned work")
	if ui.done != 10 || !strings.Contains(output.String(), "100% work 10/10") {
		t.Fatalf("progress stalled: %s", output.String())
	}
	ui.advance(100, "defensive bound")
	if ui.done != 10 {
		t.Fatal("progress exceeded total")
	}
}

func TestFormatBytesKeepsFractionalUnits(t *testing.T) {
	for size, want := range map[int]string{999: "999B", 1000: "1kB", 1500: "1.5kB", 1000000: "1MB", 1500000: "1.5MB", 25000000: "25MB"} {
		if got := formatBytes(size); got != want {
			t.Errorf("formatBytes(%d)=%q, want%q", size, got, want)
		}
	}
}

func TestRunCLIEntryWrapper(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"-version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("offline entry returned %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), version) || stderr.Len() != 0 {
		t.Fatalf("unexpected offline entry output: %s %s", stdout.String(), stderr.String())
	}
}

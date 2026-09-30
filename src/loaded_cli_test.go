package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLILoadedFailureAndFastTransferAvailability(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delay       time.Duration
		wantCode    int
		wantFailure bool
	}{
		{name: "eligible transfer with failed side probe", delay: 280 * time.Millisecond, wantCode: 1, wantFailure: true},
		{name: "fast transfer with no eligible loaded data", delay: 15 * time.Millisecond, wantCode: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var loadedRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/locations":
					_, _ = io.WriteString(w, `[{"iata":"LOCAL","city":"Local fixture"}]`)
				case "/cdn-cgi/trace":
					_, _ = io.WriteString(w, "ip=127.0.0.1\nloc=TEST\ncolo=LOCAL\n")
				case "/__down":
					if r.URL.Query().Get("during") == "download" {
						loadedRequests.Add(1)
						if tc.wantFailure {
							w.WriteHeader(http.StatusServiceUnavailable)
						} else {
							w.Header().Set("Content-Length", "0")
						}
						return
					}
					if r.URL.Query().Get("bytes") == "0" {
						w.Header().Set("Content-Length", "0")
						return
					}
					time.Sleep(tc.delay)
					_, _ = io.WriteString(w, strings.Repeat("x", 1000))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			code := runCLIContext(context.Background(), []string{"-download", "-packet-loss=false"}, &stdout, &stderr, cliConfig{baseURL: server.URL, phases: []phase{{Direction: "latency", Count: 2}, {Direction: "download", Bytes: 1000, Count: 1}}})
			if code != tc.wantCode {
				t.Fatalf("exit%d, wanted%d: %s", code, tc.wantCode, stderr.String())
			}
			if tc.wantFailure && (loadedRequests.Load() == 0 || !strings.Contains(stderr.String(), "loaded latency")) {
				t.Fatalf("loaded failure hidden: %s", stderr.String())
			}
			if !tc.wantFailure && stderr.Len() != 0 {
				t.Fatalf("fast samples falsely treated as failed: %s", stderr.String())
			}
			if !strings.Contains(stdout.String(), "Download loaded latency: N/A") {
				t.Fatalf("unavailable loaded latency not represented: %s", stdout.String())
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type trackedMetadataBody struct {
	io.Reader
	readBytes int
	closed    bool
	closeErr  error
}

func (body *trackedMetadataBody) Read(buffer []byte) (int, error) {
	n, err := body.Reader.Read(buffer)
	body.readBytes += n
	return n, err
}
func (body *trackedMetadataBody) Close() error { body.closed = true; return body.closeErr }

func TestMetadataBoundsAndChecksCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		status        int
		closeErr      error
		wantError     bool
	}{
		{name: "success", content: "valid", status: 200},
		{name: "status", content: "error", status: 503, wantError: true},
		{name: "close failure", content: "valid", status: 200, closeErr: errors.New("close failure"), wantError: true},
		{name: "oversized", content: strings.Repeat("x", 2*1024*1024+100), status: 200, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedMetadataBody{Reader: strings.NewReader(tc.content), closeErr: tc.closeErr}
			client := newMeasurementClient("http://example.invalid", time.Second)
			client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("User-Agent") != userAgent {
					t.Fatal("metadata UA missing")
				}
				if _, ok := req.Context().Deadline(); !ok {
					t.Fatal("metadata deadline missing")
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body}, nil
			})
			_, err := fetchBody(context.Background(), client, "/locations", time.Second)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
			if !body.closed || body.readBytes > 2*1024*1024+1 {
				t.Fatalf("unbounded/unclosed metadata body: %+v", body)
			}
		})
	}
}

func TestTTYProgressClearedBeforeResultLines(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		print      func(*textUI)
	}{
		{name: "heading", want: "Results\n", print: func(ui *textUI) { ui.heading("Results") }},
		{name: "metric", want: "Latency: 1.00 ms\n", print: func(ui *textUI) { ui.metric("Latency", 1, true, "ms") }},
		{name: "missing metric", want: "Latency: N/A\n", print: func(ui *textUI) { ui.metric("Latency", 0, false, "ms") }},
		{name: "phase", want: "Download 1kB median: N/A", print: func(ui *textUI) {
			ui.phaseResult(phaseResult{Phase: phase{Direction: "download", Bytes: 1000, Count: 1}, Attempts: 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			ui := &textUI{stdout: &output, stderr: &output, progress: true, total: 10}
			ui.advance(1, "pending")
			tc.print(ui)
			if !strings.Contains(output.String(), "\r\033[K"+tc.want) {
				t.Fatalf("result appended to progress: %q", output.String())
			}
		})
	}
}

func TestCredentialResponseCloseFailureIsSanitized(t *testing.T) {
	body := &trackedMetadataBody{
		Reader:   strings.NewReader(`{"username":"fixture-user","credential":"fixture-token"}`),
		closeErr: errors.New("private-close-detail fixture-token"),
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}
	cfg := defaultPacketLossConfig("http://example.invalid", time.Second)
	credentials, err := fetchPacketLossCredentials(context.Background(), client, cfg)
	if err == nil || !body.closed || credentials != (packetLossCredentials{}) {
		t.Fatal("credential cleanup failure was accepted")
	}
	if strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "private-close-detail") {
		t.Fatal("credential cleanup exposed private error detail")
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestDefaultPacketLossPhasePosition(t *testing.T) {
	plan := defaultTestPhases()
	if plan[7].Direction != "upload" || plan[7].Bytes != 100000 || plan[8].Direction != "latency" || plan[9].Direction != "packetloss" || plan[9].Count != 1 || plan[10].Direction != "upload" || plan[10].Bytes != 1000000 {
		t.Fatalf("packet loss not at official phase boundary: %+v", plan[7:11])
	}
	opts, err := parseOptions([]string{"-packet-loss=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selectedPhases(plan, opts) {
		if p.Direction == "packetloss" {
			t.Fatal("disabled packet loss remains scheduled")
		}
	}
}

func packetCLIPlan() []phase {
	plan := smallCLIPlan()
	return append(plan[:2:2], append([]phase{{Direction: "packetloss", Count: 1}}, plan[2:]...)...)
}

func TestCLIPacketLossSuccessAndDisabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			server, _, _ := cliFixture(t, false, false)
			called := 0
			var stdout, stderr bytes.Buffer
			args := []string{"-loaded-latency=false"}
			if !enabled {
				args = append(args, "-packet-loss=false")
			}
			code := runCLIContext(context.Background(), args, &stdout, &stderr, cliConfig{
				baseURL: server.URL, phases: packetCLIPlan(), packetLoss: func(ctx context.Context, client *http.Client, cfg packetLossConfig, onSent func(int)) packetLossResult {
					called++
					if cfg.Count != 1000 || cfg.CredentialsURL != server.URL+"/turn-creds" {
						t.Fatalf("incorrect default loss config: %+v", cfg)
					}
					onSent(cfg.Count)
					return packetLossResult{Sent: 1000, Received: 990, Ratio: 0.01, Available: true}
				},
			})
			if code != 0 {
				t.Fatalf("exit=%d: %s", code, stderr.String())
			}
			if enabled && (called != 1 || !strings.Contains(stdout.String(), "Packet loss: 1.00 %")) {
				t.Fatalf("packet loss omitted: %s", stdout.String())
			}
			if !enabled && (called != 0 || strings.Contains(stdout.String(), "Packet loss:")) {
				t.Fatalf("disabled loss still executed: %s", stdout.String())
			}
			if !strings.Contains(stdout.String(), "Network Quality Score") || !strings.Contains(stdout.String(), "streaming: N/A") {
				t.Fatalf("missing loaded samples fabricated a score: %s", stdout.String())
			}
		})
	}
}

func TestCLIPacketLossFailureContinuesHTTP(t *testing.T) {
	server, down, up := cliFixture(t, false, false)
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-loaded-latency=false"}, &stdout, &stderr, cliConfig{
		baseURL: server.URL, phases: packetCLIPlan(), packetLoss: func(context.Context, *http.Client, packetLossConfig, func(int)) packetLossResult {
			return packetLossResult{Err: errors.New("TURN unavailable")}
		},
	})
	if code != 1 || down.Load() != 2 || up.Load() != 2 {
		t.Fatalf("loss failure stopped bandwidth tests: exit%d down%d up%d", code, down.Load(), up.Load())
	}
	if !strings.Contains(stdout.String(), "Packet loss: N/A") || !strings.Contains(stderr.String(), "packet loss unavailable") || !strings.Contains(stdout.String(), "Upload speed (P90):") {
		t.Fatalf("incorrect partial failure: %s %s", stdout.String(), stderr.String())
	}
}

func TestTurnCredentialsURLValidationDoesNotEchoSecrets(t *testing.T) {
	for _, endpoint := range []string{"ftp://example.com/credentials", "/relative", "https://private-user:private-password@example.com/", "https://example.com/#fragment"} {
		var stdout, stderr bytes.Buffer
		code := runCLIContext(context.Background(), []string{"-turn-creds-url=" + endpoint}, &stdout, &stderr, cliConfig{})
		if code != 2 {
			t.Fatalf("invalid credentials endpoint returned%d", code)
		}
		if strings.Contains(stderr.String(), "private-user") || strings.Contains(stderr.String(), "private-password") {
			t.Fatal("credentials echoed in URL validation")
		}
	}
}

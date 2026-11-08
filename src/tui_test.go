package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func tuiTestOptions(t *testing.T, args ...string) cliOptions {
	t.Helper()
	opts, err := parseOptions(append([]string{"-lite", "-loaded-latency=false", "-packet-loss=false", "-no-color"}, args...), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return opts
}

func finishTUIRun(t *testing.T, m *tuiModel) tea.Cmd {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case message := <-m.events:
			_, command := m.Update(message)
			if _, ok := message.(tuiFinishedMsg); ok {
				select {
				case <-m.workerDone:
				default:
					t.Fatal("completion arrived before worker cleanup")
				}
				return command
			}
		case <-deadline.C:
			m.cancel()
			t.Fatal("TUI worker did not finish")
		}
	}
}

func TestTUIWorkerSharesReducersAndRerunsWithoutMixingSamples(t *testing.T) {
	server, down, up := cliFixture(t, false, false)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	m := newTUIModel(context.Background(), lifetime, tuiTestOptions(t), cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	m.startRun()
	finishTUIRun(t, m)
	if m.code != 0 || m.state() != "Complete" || m.progress.Done != m.progress.Total {
		t.Fatalf("wrong completion: code=%d state=%s progress=%+v", m.code, m.state(), m.progress)
	}
	if len(m.phases) != 3 || len(m.samples[measurementDownload]) != 2 || down.Load() != 2 || up.Load() != 2 {
		t.Fatal("measurement events were lost or duplicated")
	}
	result := <-m.result
	for _, direction := range []string{measurementDownload, measurementUpload} {
		value, ok := bandwidthBps(m.samples[direction])
		if !ok || !strings.Contains(result.Stdout, fmt.Sprintf("%s speed (P90): %.2f Mbps", directionLabel(direction), value/1e6)) {
			t.Fatalf("TUI and text disagree: %s", result.Stdout)
		}
	}
	if !m.summary().DownloadBps.Valid || m.summary().PacketLoss.Valid {
		t.Fatal("wrong availability in dashboard summary")
	}
	m.Update(tea.KeyPressMsg{Code: 'r'})
	finishTUIRun(t, m)
	if m.code != 0 || len(m.phases) != 3 || len(m.samples[measurementDownload]) != 2 || down.Load() != 4 || up.Load() != 4 {
		t.Fatal("rerun overlapped or retained previous samples")
	}
}

func TestTUICancelAndQuitJoinWorkerCleanup(t *testing.T) {
	for _, key := range []rune{'c', 'q', 0} {
		t.Run(fmt.Sprintf("key-%d", key), func(t *testing.T) {
			server, _, _ := cliFixture(t, false, false)
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			started := make(chan struct{})
			var cleaned atomic.Bool
			opts := tuiTestOptions(t, "-packet-loss=true")
			m := newTUIModel(parent, lifetime, opts, cliConfig{baseURL: server.URL,
				phases: []phase{{Direction: "packetloss", Count: 1}},
				packetLoss: func(ctx context.Context, _ *http.Client, _ packetLossConfig, _ func(int)) packetLossResult {
					close(started)
					<-ctx.Done()
					cleaned.Store(true)
					return packetLossResult{Err: ctx.Err()}
				},
			})
			m.startRun()
			select {
			case <-started:
			case <-time.After(time.Second):
				m.cancel()
				t.Fatal("packet-loss fixture did not start")
			}
			if key == 0 {
				cancelParent()
			} else {
				_, command := m.Update(tea.KeyPressMsg{Code: key})
				if command != nil || !m.canceling {
					t.Fatal("quit must wait for worker cleanup")
				}
			}
			command := finishTUIRun(t, m)
			if !cleaned.Load() || m.code != 130 || m.state() != "Canceled" {
				t.Fatalf("canceled work looks successful: %+v", m)
			}
			if key == 'c' && command != nil {
				t.Fatal("cancel should leave results open for inspection")
			}
			if key != 'c' {
				if command == nil {
					t.Fatal("quit or external cancellation did not exit")
				}
				if _, ok := command().(tea.QuitMsg); !ok {
					t.Fatal("expected terminal restoration after cleanup")
				}
			}
		})
	}
}

func TestTUIFailureIsIncompleteAndWarningsAreVisible(t *testing.T) {
	server, _, _ := cliFixture(t, true, true)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	m := newTUIModel(context.Background(), lifetime, tuiTestOptions(t), cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	m.startRun()
	finishTUIRun(t, m)
	if m.code != 1 || m.state() != "Incomplete" || len(m.warnings) == 0 || m.summary().DownloadBps.Valid {
		t.Fatal("failed measurements became successful dashboard data")
	}
	m.Update(tea.KeyPressMsg{Code: '3'})
	if !strings.Contains(m.View().Content, "unavailable") {
		t.Fatal("warnings panel did not expose measurement failure")
	}
}

func TestTUIFitsResizeAndScrollsMeasurementDetails(t *testing.T) {
	m := newTUIModel(context.Background(), context.Background(), tuiTestOptions(t), cliConfig{})
	for range 12 {
		m.Update(tuiPhaseMsg{Phase: phase{Direction: measurementDownload, Bytes: 1000, Count: 2}, Attempts: 2,
			Samples: []sample{{Direction: measurementDownload, SpeedBps: 1e6, DurationMs: 20}, {Direction: measurementDownload, SpeedBps: 3e6, DurationMs: 20}}})
	}
	m.Update(tea.KeyPressMsg{Code: '2'})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.viewport.YOffset() == 0 {
		t.Fatal("measurement details did not scroll")
	}
	for _, size := range [][2]int{{140, 40}, {100, 30}, {80, 24}, {40, 14}, {20, 8}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for panel := range 3 {
			m.panel = panel
			m.refresh()
			view := m.View()
			if !view.AltScreen || strings.Contains(view.Content, "\x1b") {
				t.Fatal("alternate screen or no-color contract broken")
			}
			if size[0] >= 16 && !strings.Contains(view.Content, "q quit") {
				t.Fatalf("%v: quit hint was clipped", size)
			}
			lines := strings.Split(view.Content, "\n")
			if len(lines) > size[1] {
				t.Fatalf("%v: too many rows: %d", size, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > max(1, size[0]-1) || lipgloss.Width(line) > max(1, size[0]-1) {
					t.Fatalf("%v: row wraps: %q", size, line)
				}
			}
		}
	}
}

func TestTUIFiltersSamplesAndSanitizesRemoteText(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	opts := tuiTestOptions(t, "-no-color=false", "-packet-loss=true")
	m := newTUIModel(context.Background(), context.Background(), opts, cliConfig{})
	for _, s := range []sample{
		{Direction: measurementDownload, SpeedBps: 100e6, DurationMs: 9},
		{Direction: measurementDownload, SpeedBps: math.Inf(1), DurationMs: 20},
		{Direction: measurementDownload, SpeedBps: 2e6, DurationMs: 20},
		{Direction: measurementLatency, LatencyMs: 0},
	} {
		m.Update(tuiSampleMsg(s))
	}
	m.Update(tuiMetadataMsg{Location: "\x1b[2J台北\n\a", IP: "127.0.0.1\r"})
	m.Update(tuiWarningMsg("\x1b]52;c;fixture\aBad\nresponse"))
	if m.metadata.Location != "台北" || m.metadata.IP != "127.0.0.1" || m.warnings[0] != "Badresponse" {
		t.Fatalf("terminal controls escaped sanitization: %+v %q", m.metadata, m.warnings)
	}
	summary := m.summary()
	if summary.DownloadBps.Value != 2e6 || !summary.LatencyMs.Valid || summary.JitterMs.Valid || summary.PacketLoss.Valid {
		t.Fatalf("wrong reducer availability: %+v", summary)
	}
	if strings.Contains(m.View().Content, "\x1b") || !strings.Contains(m.speedPanel(measurementDownload, 60), "2.00 Mbps") {
		t.Fatal("NO_COLOR or eligible-sample display broken")
	}
	m.Update(tuiLossMsg{Available: true, Ratio: 0})
	if !m.summary().PacketLoss.Valid || tuiMetric(m.summary().PacketLoss, 100, "%") != "0.00 %" {
		t.Fatal("measured zero loss was confused with unavailable loss")
	}
}

func TestTUIFlagFallsBackWithoutTerminalCodes(t *testing.T) {
	server, _, _ := cliFixture(t, false, false)
	var stdout, stderr bytes.Buffer
	code := runCLIContext(context.Background(), []string{"-tui", "-lite", "-packet-loss=false", "-loaded-latency=false"}, &stdout, &stderr,
		cliConfig{baseURL: server.URL, phases: smallCLIPlan()})
	if code != 0 || !strings.Contains(stdout.String(), "Test complete.") || !strings.Contains(stderr.String(), "using plain output") {
		t.Fatalf("redirected TUI did not fall back: %d %s %s", code, stdout.String(), stderr.String())
	}
	if strings.ContainsAny(stdout.String()+stderr.String(), "\r\x1b") {
		t.Fatal("redirected TUI emitted terminal codes")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCLIContext(context.Background(), []string{"-tui", "-version"}, &stdout, &stderr, cliConfig{baseURL: "http://127.0.0.1:1"}); code != 0 || stderr.Len() != 0 {
		t.Fatal("offline version tried to start TUI")
	}
}

func TestTUICurveAndMetricsHandleUnavailableValues(t *testing.T) {
	for _, values := range [][]float64{nil, {1}, {1, 8, 3, 6}, {math.NaN(), math.Inf(1)}} {
		for _, width := range []int{1, 20, 40} {
			curve := fitTUI(tuiCurve(values, width, 5), width, 5)
			for _, line := range strings.Split(curve, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatal("chart overflowed its panel")
				}
			}
		}
	}
	if !strings.Contains(tuiCurve([]float64{1, 8, 3, 6}, 40, 5), "-") {
		t.Fatal("chart is missing its P90 reference line")
	}
	if tuiMetric(optionalMetric{Value: math.NaN(), Valid: true}, 1, "ms") != "N/A" {
		t.Fatal("nonfinite metric appeared as a valid value")
	}
}

func TestTUIProgressDoesNotBlockPacketSending(t *testing.T) {
	server, _, _ := cliFixture(t, false, false)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	sentAll := make(chan struct{})
	m := newTUIModel(context.Background(), lifetime, tuiTestOptions(t, "-packet-loss=true"), cliConfig{
		baseURL: server.URL, phases: packetCLIPlan(),
		packetLoss: func(_ context.Context, _ *http.Client, cfg packetLossConfig, onSent func(int)) packetLossResult {
			for sent := 1; sent <= cfg.Count; sent++ {
				onSent(sent)
			}
			close(sentAll)
			return packetLossResult{Sent: cfg.Count, Received: cfg.Count, Available: true}
		},
	})
	m.startRun()
	// Leave the renderer idle while packet-send callbacks fill the queue.
	select {
	case <-sentAll:
	case <-time.After(3 * time.Second):
		m.cancel()
		t.Fatal("progress updates blocked packet sending")
	}
	finishTUIRun(t, m)
	if m.code != 0 || m.progress.Done != m.progress.Total || !m.summary().PacketLoss.Valid {
		t.Fatal("coalescing progress lost final data or completion")
	}
}

func TestTUILoadedJitterLabelsIdentifyDirection(t *testing.T) {
	m := newTUIModel(context.Background(), context.Background(), tuiTestOptions(t, "-loaded-latency=true"), cliConfig{})
	text := m.overview(140)
	for _, label := range []string{"Download loaded jitter:", "Upload loaded jitter:"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing direction-specific label %q in %s", label, text)
		}
	}
}

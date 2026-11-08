package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

var version = "0.3.0-dev"

const chromeMajorVersion = "157"
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + chromeMajorVersion + ".0.0.0 Safari/537.36"
const cloudflareBaseURL = "https://speed.cloudflare.com"

type CfTrace struct {
	IP   string
	Loc  string
	Colo string
}

type Location struct {
	IATA string `json:"iata"`
	City string `json:"city"`
}

type cliOptions struct {
	download      bool
	upload        bool
	lite          bool
	liteDownload  bool
	liteUpload    bool
	showVersion   bool
	loadedLatency bool
	packetLoss    bool
	turnCredsURL  string
	turnServer    string
	noProgress    bool
	noColor       bool
	tui           bool
	metricsFile   string
	timeout       time.Duration
}

type cliConfig struct {
	baseURL    string
	phases     []phase
	packetLoss func(context.Context, *http.Client, packetLossConfig, func(int)) packetLossResult
	observe    func(any)
}

func parseOptions(args []string, stderr io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("go-speed-cloudflare-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&opts.download, "download", false, "Test download speed only")
	flags.BoolVar(&opts.upload, "upload", false, "Test upload speed only")
	flags.BoolVar(&opts.lite, "lite", false, "Limit selected directions to requests up to 10MB, not total traffic")
	flags.BoolVar(&opts.liteDownload, "lite-download", false, "Select lite download unless directions are explicitly selected")
	flags.BoolVar(&opts.liteUpload, "lite-upload", false, "Select lite upload unless directions are explicitly selected")
	flags.BoolVar(&opts.showVersion, "version", false, "Show version without network activity")
	flags.BoolVar(&opts.loadedLatency, "loaded-latency", true, "Measure latency during bandwidth transfers")
	flags.BoolVar(&opts.packetLoss, "packet-loss", true, "Measure WebRTC message loss through UDP TURN relay")
	flags.StringVar(&opts.turnCredsURL, "turn-creds-url", "", "Override TURN credentials endpoint, using username/credential plus server or UDP urls")
	flags.StringVar(&opts.turnServer, "turn-server", "", "Fallback TURN host:port when the credentials response has no server or urls")
	flags.BoolVar(&opts.noProgress, "no-progress", false, "Disable terminal progress on stderr")
	flags.BoolVar(&opts.noColor, "no-color", false, "Disable terminal colors")
	flags.BoolVar(&opts.tui, "tui", false, "Show an interactive dashboard; fall back to text when redirected")
	flags.StringVar(&opts.metricsFile, "metrics-file", "", "Write final Prometheus metrics to a .prom file (parent directory must exist)")
	flags.DurationVar(&opts.timeout, "timeout", 30*time.Second, "Whole-request deadline, including 429 retry waits")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, `Usage: go-speed-cloudflare-cli [flags]

Modes (directions can be combined):
  (no flags)       Full download + upload (~1.266 GB planned payload)
  -lite            Lite download + upload (~115.8 MB planned payload)
  -download        Download only
  -upload          Upload only
  -lite-download   Lite download only
  -lite-upload     Lite upload only

Lite limits request sizes to 10 MB, not total traffic.
Loaded latency and UDP packet loss are enabled by default.

Examples:
  go-speed-cloudflare-cli -lite
  go-speed-cloudflare-cli -download -packet-loss=false
  go-speed-cloudflare-cli -download -upload -lite-download
  go-speed-cloudflare-cli -tui -lite

Flags:`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if opts.showVersion {
		return opts, nil
	}
	if opts.timeout <= 0 {
		return opts, errors.New("timeout must be greater than zero")
	}
	if opts.turnCredsURL != "" {
		endpoint, err := url.Parse(opts.turnCredsURL)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return opts, errors.New("TURN credentials URL must be an absolute HTTP(S) URL without user information or fragment")
		}
	}
	directionSpecified := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "download" || f.Name == "upload" {
			directionSpecified = true
		}
	})
	if directionSpecified {
		if !opts.download && !opts.upload {
			return opts, errors.New("at least one direction must be selected")
		}
	} else if opts.liteDownload || opts.liteUpload {
		opts.download, opts.upload = opts.liteDownload, opts.liteUpload
	} else {
		opts.download, opts.upload = true, true
	}
	if opts.liteDownload && !opts.download {
		return opts, errors.New("-lite-download requires download to be selected")
	}
	if opts.liteUpload && !opts.upload {
		return opts, errors.New("-lite-upload requires upload to be selected")
	}
	return opts, nil
}

func defaultTestPhases() []phase {
	httpPlan := defaultPhases()
	plan := make([]phase, 0, len(httpPlan)+1)
	plan = append(plan, httpPlan[:9]...)
	plan = append(plan, phase{Direction: "packetloss", Count: 1})
	return append(plan, httpPlan[9:]...)
}

func selectedPhases(plan []phase, opts cliOptions) []phase {
	selected := make([]phase, 0, len(plan))
	for _, p := range plan {
		switch p.Direction {
		case "download":
			if !opts.download || (opts.lite || opts.liteDownload) && p.Bytes > 10000000 {
				continue
			}
		case "upload":
			if !opts.upload || (opts.lite || opts.liteUpload) && p.Bytes > 10000000 {
				continue
			}
		case "packetloss":
			if !opts.packetLoss {
				continue
			}
		}
		selected = append(selected, p)
	}
	return selected
}

func directionLabel(direction string) string {
	switch direction {
	case "download":
		return "Download"
	case "upload":
		return "Upload"
	default:
		return "Latency"
	}
}

func formatBytes(size int) string {
	switch {
	case size >= 1000000:
		return fmt.Sprintf("%gMB", float64(size)/1000000)
	case size >= 1000:
		return fmt.Sprintf("%gkB", float64(size)/1000)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

type textUI struct {
	stdout   io.Writer
	stderr   io.Writer
	progress bool
	color    bool
	done     int
	total    int
	observe  func(any)
}

func (ui *textUI) planSummary(opts cliOptions, plan []phase) {
	var modes []string
	for _, direction := range []string{"download", "upload"} {
		if direction == "download" && !opts.download || direction == "upload" && !opts.upload {
			continue
		}
		mode := "full"
		if opts.lite || direction == "download" && opts.liteDownload || direction == "upload" && opts.liteUpload {
			mode = "lite"
		}
		modes = append(modes, directionLabel(direction)+" ("+mode+")")
	}
	var payload int
	for _, p := range plan {
		if p.Direction == "download" || p.Direction == "upload" {
			payload += p.Bytes * p.Count
		}
	}
	_, _ = fmt.Fprintf(ui.stdout, "Mode: %s\nPlanned HTTP payload: %s (before early finish/retries; not a cap)\n", strings.Join(modes, " + "), formatBytes(payload))
	loaded, loss := "off", "off"
	if opts.loadedLatency {
		loaded = "on"
	}
	if opts.packetLoss {
		loss = "on (UDP TURN)"
	}
	_, _ = fmt.Fprintf(ui.stdout, "Loaded latency: %s | Packet loss: %s\n\n", loaded, loss)
	if ui.progress {
		_, _ = fmt.Fprintln(ui.stderr, "Progress tracks planned work, including skips; not time remaining. Ctrl+C to cancel.")
	}
}

func (ui *textUI) emit(event any) {
	if ui.observe != nil {
		ui.observe(event)
	}
}

func (ui *textUI) clearProgress() {
	if ui.progress {
		_, _ = fmt.Fprint(ui.stderr, "\r\033[K")
	}
}

func (ui *textUI) advance(units int, label string) {
	ui.done += units
	if ui.done > ui.total {
		ui.done = ui.total
	}
	ui.emit(tuiProgressMsg{Done: ui.done, Total: ui.total, Label: label})
	if !ui.progress || ui.total == 0 {
		return
	}
	width := 80
	if file, ok := ui.stderr.(*os.File); ok {
		if columns, _, err := term.GetSize(int(file.Fd())); err == nil && columns > 0 {
			width = columns
		}
	}
	_, _ = fmt.Fprint(ui.stderr, "\r\033[K"+ui.progressLine(label, width))
}

func (ui *textUI) progressLine(label string, width int) string {
	percent := 100 * ui.done / ui.total
	barWidth := 16
	if width < 100 {
		barWidth = 8
	}
	filled := barWidth * ui.done / ui.total
	prefix := fmt.Sprintf("[%s%s] %3d%% work %d/%d | ", strings.Repeat("#", filled), strings.Repeat("-", barWidth-filled), percent, ui.done, ui.total)
	if width < 60 {
		prefix = fmt.Sprintf("%3d%% | ", percent)
	}
	line := prefix + label
	// Leave the last column empty to avoid terminal auto-wrap.
	limit := max(0, width-1)
	if len(line) > limit {
		if limit >= 3 {
			return line[:limit-3] + "..."
		}
		return line[:limit]
	}
	return line
}

func (ui *textUI) warning(format string, args ...any) {
	ui.emit(tuiWarningMsg(fmt.Sprintf(format, args...)))
	ui.clearProgress()
	_, _ = fmt.Fprintf(ui.stderr, "Warning: "+format+"\n", args...)
}

func (ui *textUI) heading(text string) {
	ui.clearProgress()
	if ui.color {
		text = Bold(text)
	}
	_, _ = fmt.Fprintln(ui.stdout, text)
}

func (ui *textUI) metric(label string, value float64, ok bool, unit string) {
	ui.clearProgress()
	if !ok {
		_, _ = fmt.Fprintf(ui.stdout, "%s: N/A\n", label)
		return
	}
	formatted := fmt.Sprintf("%.2f %s", value, unit)
	if ui.color {
		formatted = Green(formatted)
	}
	_, _ = fmt.Fprintf(ui.stdout, "%s: %s\n", label, formatted)
}

func (ui *textUI) phaseResult(result phaseResult) {
	ui.emit(tuiPhaseMsg(result))
	if result.Phase.Direction == "latency" {
		return
	}
	ui.clearProgress()
	label := fmt.Sprintf("%s %s median", directionLabel(result.Phase.Direction), formatBytes(result.Phase.Bytes))
	values := make([]float64, 0, len(result.Samples))
	for _, s := range result.Samples {
		if s.SpeedBps > 0 && s.DurationMs >= 10 {
			values = append(values, s.SpeedBps/1e6)
		}
	}
	if len(values) == 0 {
		_, _ = fmt.Fprintf(ui.stdout, "%s: N/A (%d/%d successful attempts)\n", label, len(result.Samples), result.Attempts)
		return
	}
	_, _ = fmt.Fprintf(ui.stdout, "%s: %.2f Mbps (%d/%d valid samples)\n", label, median(values), len(values), result.Attempts)
}

func fetchBody(ctx context.Context, client *measurementClient, path string, timeout time.Duration) (body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(client.baseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}
	const limit = 2 * 1024 * 1024
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%s response exceeds %d bytes", path, limit)
	}
	return body, nil
}

func showMetadata(ctx context.Context, client *measurementClient, opts cliOptions, ui *textUI) {
	cities := make(map[string]string)
	ui.advance(0, "Finding server locations")
	body, err := fetchBody(ctx, client, "/locations", opts.timeout)
	if err == nil {
		var locations []Location
		err = json.Unmarshal(body, &locations)
		for _, location := range locations {
			cities[location.IATA] = location.City
		}
	}
	if err != nil {
		ui.warning("server locations unavailable: %v", err)
	}
	ui.advance(1, "Reading connection metadata")
	body, err = fetchBody(ctx, client, "/cdn-cgi/trace", opts.timeout)
	var trace CfTrace
	if err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			switch key {
			case "ip":
				trace.IP = value
			case "loc":
				trace.Loc = value
			case "colo":
				trace.Colo = value
			}
		}
		if net.ParseIP(trace.IP) == nil || trace.Loc == "" || trace.Colo == "" {
			err = errors.New("trace response is missing valid ip, loc or colo")
		}
	}
	ui.clearProgress()
	if err != nil {
		ui.warning("connection metadata unavailable: %v", err)
		_, _ = fmt.Fprintln(ui.stdout, "Server location: N/A\nYour IP: N/A")
		ui.emit(tuiMetadataMsg{Location: "N/A", IP: "N/A"})
	} else {
		city := cities[trace.Colo]
		if city == "" {
			city = "Unknown city"
		}
		_, _ = fmt.Fprintf(ui.stdout, "Server location: %s (%s)\nYour IP: %s (%s)\n", city, trace.Colo, trace.IP, trace.Loc)
		ui.emit(tuiMetadataMsg{Location: fmt.Sprintf("%s (%s)", city, trace.Colo), IP: trace.IP})
	}
	ui.advance(1, "connection metadata")
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	return runCLIContext(context.Background(), args, stdout, stderr, cliConfig{})
}

func runCLIContext(ctx context.Context, args []string, stdout, stderr io.Writer, config cliConfig) int {
	opts, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	if opts.showVersion {
		_, _ = fmt.Fprintf(stdout, "go-speed-cloudflare-cli version %s\n", version)
		return 0
	}
	if opts.metricsFile != "" {
		if _, err := metricsFileMode(opts.metricsFile); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
	}
	if opts.tui {
		if !opts.noProgress && os.Getenv("TERM") != "dumb" && isTerminal(os.Stdin) && isTerminal(stdout) && isTerminal(stderr) {
			return runTUIContext(ctx, opts, stdout, stderr, os.Stdin, config)
		}
		_, _ = fmt.Fprintln(stderr, "Warning: TUI needs an interactive terminal and progress enabled; using plain output")
	}
	return runMeasurements(ctx, opts, stdout, stderr, config)
}

func runMeasurements(ctx context.Context, opts cliOptions, stdout, stderr io.Writer, config cliConfig) int {
	if config.baseURL == "" {
		config.baseURL = cloudflareBaseURL
	}
	if config.phases == nil {
		config.phases = defaultTestPhases()
	}
	plan := selectedPhases(config.phases, opts)
	_, noColorEnv := os.LookupEnv("NO_COLOR")
	terminalControl := os.Getenv("TERM") != "dumb"
	ui := &textUI{stdout: stdout, stderr: stderr, progress: terminalControl && isTerminal(stderr) && !opts.noProgress, color: terminalControl && isTerminal(stdout) && !opts.noColor && !noColorEnv, total: 2, observe: config.observe}
	for _, p := range plan {
		ui.total += p.Count
	}
	defer func() {
		ui.clearProgress()
		if ui.progress {
			_, _ = fmt.Fprintln(stderr)
		}
	}()
	client := newMeasurementClient(config.baseURL, opts.timeout)
	defer client.client.CloseIdleConnections()
	ui.heading("Cloudflare Speed Test (Go CLI)")
	_, _ = fmt.Fprintf(stdout, "Version: %s\n", version)
	ui.planSummary(opts, plan)
	showMetadata(ctx, client, opts, ui)
	results := make([]phaseResult, 0, len(plan))
	finished := make(map[string]bool)
	failed := false
	var loss packetLossResult
	packetLossRunner := config.packetLoss
	if packetLossRunner == nil {
		packetLossRunner = measurePacketLoss
	}
	for _, p := range plan {
		if ctx.Err() != nil || finished[p.Direction] {
			ui.advance(p.Count, "skipped planned work")
			continue
		}
		if p.Direction == "packetloss" {
			ui.advance(0, "Packet loss: connecting to UDP relay")
			cfg := defaultPacketLossConfig(config.baseURL, opts.timeout)
			if opts.turnCredsURL != "" {
				cfg.CredentialsURL = opts.turnCredsURL
			}
			if opts.turnServer != "" {
				cfg.TURNServer = opts.turnServer
			}
			loss = packetLossRunner(ctx, client.client, cfg, func(sent int) {
				ui.advance(0, fmt.Sprintf("Packet loss: %d/%d sent", sent, cfg.Count))
			})
			ui.emit(tuiLossMsg(loss))
			if loss.Err != nil || !loss.Available {
				failed = true
				ui.warning("packet loss unavailable: %v", loss.Err)
			}
			ui.advance(p.Count, "packet loss session")
			continue
		}
		label := directionLabel(p.Direction)
		if p.Direction != "latency" {
			label += " " + formatBytes(p.Bytes)
		}
		ui.advance(0, fmt.Sprintf("%s: starting %d samples", label, p.Count))
		result := client.runPhase(ctx, p, opts.loadedLatency && p.Direction != "latency", func(attempt int, s *sample, err error) {
			if err != nil {
				ui.warning("%s sample %d/%d failed: %v", label, attempt, p.Count, err)
			}
			status := fmt.Sprintf("%s %d/%d", label, attempt, p.Count)
			if err != nil {
				status += " failed"
			} else if s != nil {
				ui.emit(tuiSampleMsg(*s))
				if p.Direction == "latency" {
					status += fmt.Sprintf(" | last %.2f ms", s.LatencyMs)
				} else if s.DurationMs >= 10 && finitePositive(s.SpeedBps) {
					status += fmt.Sprintf(" | last %.2f Mbps", s.SpeedBps/1e6)
				} else {
					status += " | last N/A"
				}
			}
			ui.advance(1, status)
		})
		if result.Attempts < p.Count {
			ui.advance(p.Count-result.Attempts, "skipped unfinished phase")
		}
		results = append(results, result)
		ui.phaseResult(result)
		if result.Err != nil || result.Failures > 0 {
			failed = true
		}
		if result.LoadedFailures > 0 {
			failed = true
			ui.warning("%s loaded latency probes failed: %v", p.Direction, result.LoadedErr)
		}
		if p.Direction != "latency" && (shouldFinish(result) || len(result.Samples) == 0) {
			finished[p.Direction] = true
		}
	}
	var idle, downloadSamples, uploadSamples []sample
	for _, result := range results {
		switch result.Phase.Direction {
		case "latency":
			idle = append(idle, result.Samples...)
		case "download":
			downloadSamples = append(downloadSamples, result.Samples...)
		case "upload":
			uploadSamples = append(uploadSamples, result.Samples...)
		}
	}
	ui.heading("Results")
	latency, jitterValue, latencyOK, jitterOK := latencyStats(idle)
	ui.metric("Unloaded latency", latency, latencyOK, "ms")
	ui.metric("Unloaded jitter", jitterValue, jitterOK, "ms")
	if !latencyOK || !jitterOK {
		failed = true
	}
	summary := qualitySummary{
		LatencyMs:  optionalMetric{Value: latency, Valid: latencyOK},
		JitterMs:   optionalMetric{Value: jitterValue, Valid: jitterOK},
		PacketLoss: optionalMetric{Value: loss.Ratio, Valid: loss.Available},
	}
	var downloadLoadedJitter, uploadLoadedJitter optionalMetric
	for _, direction := range []string{"download", "upload"} {
		if direction == "download" && !opts.download || direction == "upload" && !opts.upload {
			continue
		}
		samples := downloadSamples
		if direction == "upload" {
			samples = uploadSamples
		}
		speed, ok := bandwidthBps(samples)
		ui.metric(directionLabel(direction)+" speed (P90)", speed/1e6, ok, "Mbps")
		if direction == "download" {
			summary.DownloadBps = optionalMetric{Value: speed, Valid: ok}
		} else {
			summary.UploadBps = optionalMetric{Value: speed, Valid: ok}
		}
		if !ok {
			failed = true
		}
		if opts.loadedLatency {
			latency, jitterValue, latencyOK, jitterOK := loadedLatencyStats(results, direction)
			ui.metric(directionLabel(direction)+" loaded latency", latency, latencyOK, "ms")
			ui.metric(directionLabel(direction)+" loaded jitter", jitterValue, jitterOK, "ms")
			if direction == "download" {
				summary.DownLoadedLatencyMs = optionalMetric{Value: latency, Valid: latencyOK}
				downloadLoadedJitter = optionalMetric{Value: jitterValue, Valid: jitterOK}
			} else {
				summary.UpLoadedLatencyMs = optionalMetric{Value: latency, Valid: latencyOK}
				uploadLoadedJitter = optionalMetric{Value: jitterValue, Valid: jitterOK}
			}
		}
	}
	if opts.packetLoss {
		ui.metric("Packet loss", loss.Ratio*100, loss.Available, "%")
	}
	ui.heading("Network Quality Score")
	for _, score := range networkQualityScores(summary) {
		if score.Available {
			_, _ = fmt.Fprintf(stdout, "%s: %s (%d points)\n", score.Name, score.Classification, score.Points)
		} else {
			_, _ = fmt.Fprintf(stdout, "%s: N/A\n", score.Name)
		}
	}
	// Fix the measurement outcome before export so the snapshot and report agree.
	exitCode := 0
	if ctx.Err() != nil {
		exitCode = 130
	} else if failed {
		exitCode = 1
	}
	metricsFailed := false
	if opts.metricsFile != "" {
		snapshot := metricsSnapshot{
			Summary: summary, Download: opts.download, Upload: opts.upload,
			LoadedLatency: opts.loadedLatency, PacketLoss: opts.packetLoss,
			DownloadLoadedJitterMs: downloadLoadedJitter, UploadLoadedJitterMs: uploadLoadedJitter,
			ExitCode: exitCode, CompletedAt: time.Now(),
		}
		if err := writeMetricsFile(opts.metricsFile, snapshot); err != nil {
			metricsFailed = true
			ui.warning("Prometheus metrics export failed: %v", err)
		}
	}
	if exitCode == 130 {
		ui.warning("measurement canceled: %v", ctx.Err())
		return 130
	}
	if exitCode == 1 {
		ui.warning("measurement incomplete; unavailable values are shown as N/A")
		return 1
	}
	if metricsFailed {
		return 1
	}
	ui.heading("Test complete.")
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCLIContext(ctx, os.Args[1:], os.Stdout, os.Stderr, cliConfig{})
	stop()
	os.Exit(code)
}

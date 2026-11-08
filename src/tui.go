package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type tuiProgressMsg struct {
	Done, Total int
	Label       string
}
type tuiSampleMsg sample
type tuiPhaseMsg phaseResult
type tuiLossMsg packetLossResult
type tuiWarningMsg string
type tuiMetadataMsg struct{ Location, IP string }
type tuiFinishedMsg struct {
	Code           int
	Stdout, Stderr string
}

type tuiStyles struct{ title, download, upload, muted lipgloss.Style }

func newTUIStyles(color bool) tuiStyles {
	styles := tuiStyles{}
	if color {
		styles.title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#58C4D8"))
		styles.download = lipgloss.NewStyle().Foreground(lipgloss.Color("#E88A32"))
		styles.upload = lipgloss.NewStyle().Foreground(lipgloss.Color("#A57AE0"))
		styles.muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#808890"))
	}
	return styles
}

type tuiModel struct {
	parent, lifetime context.Context
	opts             cliOptions
	config           cliConfig
	styles           tuiStyles
	width, height    int
	panel            int
	viewport         viewport.Model
	planLines        []string
	progress         tuiProgressMsg
	metadata         tuiMetadataMsg
	samples          map[string][]sample
	phases           []phaseResult
	loss             packetLossResult
	warnings         []string
	finished         bool
	canceling        bool
	quitting         bool
	code             int
	cancel           context.CancelFunc
	events           chan tea.Msg
	workerDone       chan struct{}
	result           chan tuiFinishedMsg
}

func newTUIModel(parent, lifetime context.Context, opts cliOptions, config cliConfig) *tuiModel {
	_, noColor := os.LookupEnv("NO_COLOR")
	m := &tuiModel{parent: parent, lifetime: lifetime, opts: opts, config: config,
		styles: newTUIStyles(!opts.noColor && !noColor), width: 80, height: 24,
		viewport: viewport.New(), metadata: tuiMetadataMsg{Location: "N/A", IP: "N/A"},
		samples: make(map[string][]sample)}
	plan := config.phases
	if plan == nil {
		plan = defaultTestPhases()
	}
	plan = selectedPhases(plan, opts)
	m.progress.Total = 2
	for _, p := range plan {
		m.progress.Total += p.Count
	}
	var summary bytes.Buffer
	(&textUI{stdout: &summary, stderr: io.Discard}).planSummary(opts, plan)
	m.planLines = strings.Split(strings.TrimSpace(summary.String()), "\n")
	m.viewport.FillHeight = true
	m.refresh()
	return m
}

func (m *tuiModel) startRun() {
	m.samples = make(map[string][]sample)
	m.phases, m.warnings = nil, nil
	m.loss = packetLossResult{}
	m.metadata = tuiMetadataMsg{Location: "N/A", IP: "N/A"}
	m.progress.Done, m.progress.Label = 0, "Preparing measurements"
	m.finished, m.canceling, m.quitting, m.code = false, false, false, 0
	m.viewport.GotoTop()
	ctx, cancel := context.WithCancel(m.parent)
	m.cancel = cancel
	m.events = make(chan tea.Msg, 64)
	m.workerDone = make(chan struct{})
	m.result = make(chan tuiFinishedMsg, 1)
	// Each run owns its channels; completion follows every measurement event.
	events, done, result := m.events, m.workerDone, m.result
	lifetime, opts, config := m.lifetime, m.opts, m.config
	opts.noProgress, opts.noColor = true, true
	previousObserver := config.observe
	config.observe = func(event any) {
		if previousObserver != nil {
			previousObserver(event)
		}
		// Status refreshes must not delay packet sending if rendering falls behind.
		if _, progress := event.(tuiProgressMsg); progress {
			select {
			case events <- event:
			default:
			}
			return
		}
		select {
		case events <- event:
		case <-lifetime.Done():
		}
	}
	go func() {
		var stdout, stderr bytes.Buffer
		code := runMeasurements(ctx, opts, &stdout, &stderr, config)
		cancel()
		message := tuiFinishedMsg{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
		result <- message
		close(done)
		select {
		case events <- message:
		case <-lifetime.Done():
		}
	}()
	m.refresh()
}

func (m *tuiModel) waitEvent() tea.Cmd {
	events, lifetime := m.events, m.lifetime
	return func() tea.Msg {
		select {
		case event := <-events:
			return event
		case <-lifetime.Done():
			return nil
		}
	}
}

func (m *tuiModel) Init() tea.Cmd { return m.waitEvent() }

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.finished {
				return m, tea.Quit
			}
			m.quitting, m.canceling = true, true
			m.cancel()
		case "c":
			if !m.finished {
				m.canceling = true
				m.cancel()
			}
		case "r":
			if m.finished && m.parent.Err() == nil {
				m.startRun()
				return m, m.waitEvent()
			}
		case "tab", "right":
			m.panel = (m.panel + 1) % 3
			m.viewport.GotoTop()
		case "shift+tab", "left":
			m.panel = (m.panel + 2) % 3
			m.viewport.GotoTop()
		case "1", "2", "3":
			m.panel = int(msg.String()[0] - '1')
			m.viewport.GotoTop()
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(message)
			return m, cmd
		}
	case tuiProgressMsg:
		msg.Label = tuiSafeText(msg.Label)
		m.progress = msg
	case tuiSampleMsg:
		s := sample(msg)
		m.samples[s.Direction] = append(m.samples[s.Direction], s)
	case tuiPhaseMsg:
		m.phases = append(m.phases, phaseResult(msg))
	case tuiLossMsg:
		m.loss = packetLossResult(msg)
	case tuiMetadataMsg:
		m.metadata = tuiMetadataMsg{Location: tuiSafeText(msg.Location), IP: tuiSafeText(msg.IP)}
	case tuiWarningMsg:
		m.warnings = append(m.warnings, tuiSafeText(string(msg)))
	case tuiFinishedMsg:
		m.finished, m.code = true, msg.Code
		m.progress.Done = m.progress.Total
		m.progress.Label = "Results ready"
		m.refresh()
		if m.quitting || m.parent.Err() != nil {
			return m, tea.Quit
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(message)
		return m, cmd
	}
	m.refresh()
	switch message.(type) {
	case tuiProgressMsg, tuiSampleMsg, tuiPhaseMsg, tuiLossMsg, tuiMetadataMsg, tuiWarningMsg:
		return m, m.waitEvent()
	}
	return m, nil
}

func (m *tuiModel) state() string {
	if m.finished {
		switch m.code {
		case 0:
			return "Complete"
		case 130:
			return "Canceled"
		default:
			return "Incomplete"
		}
	}
	if m.canceling {
		return "Canceling; waiting for cleanup"
	}
	return "Running"
}

func (m *tuiModel) header() string {
	labels := []string{"1 Overview", "2 Measurements", fmt.Sprintf("3 Warnings (%d)", len(m.warnings))}
	labels[m.panel] = "[" + labels[m.panel] + "]"
	ui := &textUI{done: m.progress.Done, total: max(1, m.progress.Total)}
	return strings.Join([]string{
		m.styles.title.Render("Cloudflare Speed Test  v" + version),
		m.planLines[0],
		m.state() + " | " + m.progress.Label,
		ui.progressLine("planned work; not an ETA", max(1, m.width-1)),
		strings.Join(labels, "   "),
		strings.Repeat("-", max(0, m.width-1)),
	}, "\n")
}

func (m *tuiModel) refresh() {
	width := max(1, m.width-1)
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-8))
	var body string
	switch m.panel {
	case 0:
		body = m.overview(width)
	case 1:
		body = m.measurements()
	case 2:
		body = strings.Join(m.warnings, "\n\n")
		if body == "" {
			body = "No warnings."
		}
	}
	m.viewport.SetContent(fitTUI(body, width, 0))
}

func (m *tuiModel) View() tea.View {
	footer := "Tab panels | up/down scroll | c cancel | r run again | q quit"
	if m.width < 70 {
		footer = "Tab | c stop | r rerun | q quit"
	}
	content := m.header() + "\n" + m.viewport.View() + "\n" + m.styles.muted.Render(footer)
	if m.height < 10 || m.width < 30 {
		content = "Speed Test | " + m.state() + "\n" + m.progress.Label + "\nq quit | resize"
	}
	view := tea.NewView(fitTUI(content, max(1, m.width-1), m.height))
	view.AltScreen = true
	return view
}

func (m *tuiModel) summary() qualitySummary {
	latency, jitter, latencyOK, jitterOK := latencyStats(m.samples[measurementLatency])
	down, downOK := bandwidthBps(m.samples[measurementDownload])
	up, upOK := bandwidthBps(m.samples[measurementUpload])
	downLoaded, _, downLoadedOK, _ := loadedLatencyStats(m.phases, measurementDownload)
	upLoaded, _, upLoadedOK, _ := loadedLatencyStats(m.phases, measurementUpload)
	return qualitySummary{
		DownloadBps: optionalMetric{Value: down, Valid: downOK}, UploadBps: optionalMetric{Value: up, Valid: upOK},
		LatencyMs: optionalMetric{Value: latency, Valid: latencyOK}, JitterMs: optionalMetric{Value: jitter, Valid: jitterOK},
		DownLoadedLatencyMs: optionalMetric{Value: downLoaded, Valid: downLoadedOK}, UpLoadedLatencyMs: optionalMetric{Value: upLoaded, Valid: upLoadedOK},
		PacketLoss: optionalMetric{Value: m.loss.Ratio, Valid: m.loss.Available},
	}
}

func tuiMetric(metric optionalMetric, scale float64, unit string) string {
	if !metric.Valid || !finiteNonnegative(metric.Value) {
		return "N/A"
	}
	return fmt.Sprintf("%.2f %s", metric.Value*scale, unit)
}

func (m *tuiModel) speedPanel(direction string, width int) string {
	samples := m.samples[direction]
	p90, valid := bandwidthBps(samples)
	caption := "P90"
	if !m.finished {
		caption = "Provisional P90"
	}
	last := "N/A"
	var values []float64
	for _, s := range samples {
		if finitePositive(s.SpeedBps) && finiteNonnegative(s.DurationMs) && s.DurationMs >= 10 {
			values = append(values, s.SpeedBps/1e6)
			last = fmt.Sprintf("%.2f Mbps", s.SpeedBps/1e6)
		}
	}
	style := m.styles.download
	if direction == measurementUpload {
		style = m.styles.upload
	}
	text := directionLabel(direction) + "\n" + tuiMetric(optionalMetric{Value: p90, Valid: valid}, 1e-6, "Mbps") + "  " + caption +
		"\n" + tuiCurve(values, width, 5) + "\nLatest eligible sample: " + last + "\n- P90 | sample order | own scale"
	return style.Render(fitTUI(text, width, 0))
}

func (m *tuiModel) overview(width int) string {
	summary := m.summary()
	network := []string{"Network", "Unloaded latency: " + tuiMetric(summary.LatencyMs, 1, "ms"), "Unloaded jitter: " + tuiMetric(summary.JitterMs, 1, "ms")}
	if m.opts.loadedLatency {
		for _, direction := range []string{measurementDownload, measurementUpload} {
			if direction == measurementDownload && !m.opts.download || direction == measurementUpload && !m.opts.upload {
				continue
			}
			latency, jitter, latencyOK, jitterOK := loadedLatencyStats(m.phases, direction)
			network = append(network, directionLabel(direction)+" loaded: "+tuiMetric(optionalMetric{Value: latency, Valid: latencyOK}, 1, "ms"),
				directionLabel(direction)+" loaded jitter: "+tuiMetric(optionalMetric{Value: jitter, Valid: jitterOK}, 1, "ms"))
		}
	}
	if m.opts.packetLoss {
		network = append(network, "Packet loss: "+tuiMetric(summary.PacketLoss, 100, "%"))
	}
	var parts []string
	if m.opts.download && m.opts.upload && width >= 110 {
		column := (width - 4) / 3
		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(column).Render(m.speedPanel(measurementDownload, column)), "  ",
			lipgloss.NewStyle().Width(column).Render(m.speedPanel(measurementUpload, column)), "  ", fitTUI(strings.Join(network, "\n"), column, 0)))
	} else {
		if m.opts.download && m.opts.upload && width >= 76 {
			column := (width - 2) / 2
			parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Top,
				lipgloss.NewStyle().Width(column).Render(m.speedPanel(measurementDownload, column)), "  ", m.speedPanel(measurementUpload, column)))
		} else {
			if m.opts.download {
				parts = append(parts, m.speedPanel(measurementDownload, width))
			}
			if m.opts.upload {
				parts = append(parts, m.speedPanel(measurementUpload, width))
			}
		}
		parts = append(parts, strings.Join(network, "\n"))
	}
	var scores []string
	for _, score := range networkQualityScores(summary) {
		value := "N/A"
		if score.Available {
			value = fmt.Sprintf("%s (%d points)", score.Classification, score.Points)
		}
		scores = append(scores, score.Name+": "+value)
	}
	parts = append(parts, "Network Quality Score\n"+strings.Join(scores, "\n"),
		"Server: "+m.metadata.Location+"\nIP: "+m.metadata.IP, strings.Join(m.planLines[1:], "\n"))
	return strings.Join(parts, "\n\n")
}

func (m *tuiModel) measurements() string {
	if len(m.phases) == 0 {
		return "Waiting for completed phases. Use Overview for live samples."
	}
	var lines []string
	for _, result := range m.phases {
		label := directionLabel(result.Phase.Direction)
		if result.Phase.Direction != measurementLatency {
			label += " " + formatBytes(result.Phase.Bytes)
		}
		lines = append(lines, fmt.Sprintf("%s | %d/%d attempts | %d failed", label, result.Attempts, result.Phase.Count, result.Failures))
		if result.Phase.Direction != measurementLatency {
			p90, valid := bandwidthBps(result.Samples)
			var speeds []float64
			for _, s := range result.Samples {
				if finitePositive(s.SpeedBps) && finiteNonnegative(s.DurationMs) && s.DurationMs >= 10 {
					speeds = append(speeds, s.SpeedBps/1e6)
				}
			}
			lines = append(lines, "Median: "+tuiMetric(optionalMetric{Value: median(speeds), Valid: len(speeds) > 0}, 1, "Mbps")+
				" | Phase P90: "+tuiMetric(optionalMetric{Value: p90, Valid: valid}, 1e-6, "Mbps"))
		}
		for i, s := range result.Samples {
			value := fmt.Sprintf("%.2f ms", s.LatencyMs)
			if result.Phase.Direction != measurementLatency {
				value = tuiMetric(optionalMetric{Value: s.SpeedBps, Valid: finitePositive(s.SpeedBps) && s.DurationMs >= 10}, 1e-6, "Mbps")
			}
			lines = append(lines, fmt.Sprintf("  Sample %d: %s | duration %.2f ms", i+1, value, s.DurationMs))
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func tuiCurve(values []float64, width, height int) string {
	if len(values) == 0 {
		return "Waiting for eligible samples\n" + strings.Repeat("\n", max(0, height-2))
	}
	width, height = max(1, width), max(1, height)
	maximum := 0.0
	for _, value := range values {
		if finitePositive(value) && value > maximum {
			maximum = value
		}
	}
	if maximum == 0 {
		return "N/A"
	}
	grid := make([][]rune, height)
	for y := range grid {
		grid[y] = []rune(strings.Repeat(" ", width))
	}
	referenceY := height - 1 - int(math.Round(quartile(values, 0.9)/maximum*float64(height-1)))
	grid[max(0, min(height-1, referenceY))] = []rune(strings.Repeat("-", width))
	for x := range width {
		position := 0.0
		if width > 1 {
			position = float64(x) * float64(len(values)-1) / float64(width-1)
		}
		index := int(position)
		value := values[index]
		if index+1 < len(values) {
			value += (values[index+1] - value) * (position - float64(index))
		}
		if !finiteNonnegative(value) {
			continue
		}
		y := height - 1 - int(math.Round(value/maximum*float64(height-1)))
		grid[max(0, min(height-1, y))][x] = '*'
		if len(values) == 1 {
			break
		}
	}
	lines := make([]string, height)
	for y := range grid {
		lines[y] = string(grid[y])
	}
	return strings.Join(lines, "\n")
}

func tuiSafeText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

func fitTUI(text string, width, height int) string {
	lines := strings.Split(text, "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(1, width), "")
	}
	return strings.Join(lines, "\n")
}

func runTUIContext(ctx context.Context, opts cliOptions, stdout, stderr io.Writer, input io.Reader, config cliConfig) int {
	lifetime, stopEvents := context.WithCancel(context.Background())
	defer stopEvents()
	model := newTUIModel(ctx, lifetime, opts, config)
	model.startRun()
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(stderr), tea.WithContext(lifetime), tea.WithoutSignalHandler())
	_, err := program.Run()
	model.cancel()
	stopEvents()
	<-model.workerDone
	result := <-model.result
	_, _ = io.WriteString(stdout, result.Stdout)
	_, _ = io.WriteString(stderr, result.Stderr)
	if err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(stderr, "Warning: TUI stopped: %v\n", err)
		return 1
	}
	return result.Code
}

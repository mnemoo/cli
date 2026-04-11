package upload

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/upload"
)

type stage int

const (
	stageTypeSelect stage = iota
	stagePathInput
	stageFilePicker
	stageScanning
	stageSafetyWarn
	stageCompliance
	stageConfirm
	stageUploading
	stageDone
)

type GoBackMsg struct{}

type scanDoneMsg struct {
	files    []upload.LocalFile
	warnings []upload.SafetyWarning
	err      error
}

type planDoneMsg struct {
	plan *upload.UploadPlan
	err  error
}

type activeFile struct {
	name string
	size int64
}

type uploadProgressMsg struct {
	upload.ProgressEvent
}

type uploadFinishedMsg struct{}

type bytesTickMsg struct{}

type complianceDoneMsg struct {
	result upload.ComplianceResult
}

type publishDoneMsg struct {
	result *api.PublishResult
	err    error
}

type Model struct {
	stage      stage
	team       string
	game       string
	uploadType string
	dirPath    string
	client     *api.Client

	typeCursor int
	typeOpts   []string

	pathInput  textinput.Model
	pathErr    string
	pathFocus  int // 0 = text input, 1 = browse button
	filePicker filepicker.Model
	spinner    spinner.Model
	progress   progress.Model
	viewport   viewport.Model

	localFiles      []upload.LocalFile
	warnings        []upload.SafetyWarning
	plan            *upload.UploadPlan
	compliance      upload.ComplianceResult
	compliancePage  int
	doneCursor      int
	uploadSucceeded bool
	publishInFlight bool
	published       bool
	publishMsg      string
	publishErr      error

	// Upload state
	uploadCh        <-chan upload.ProgressEvent
	uploadCancel    context.CancelFunc
	uploadGate      *upload.PauseGate
	byteCounter     *atomic.Int64
	progressCurrent int
	progressTotal   int
	progressFile    string
	activeFiles     []activeFile
	progressErrors  []string
	startTime       time.Time
	bytesTotal      int64

	width     int
	height    int
	scanPhase string
	resultMsg string
	err       error
}

func New(client *api.Client, team, game string, width, height int) Model {
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}

	fpHeight := height - 8
	if fpHeight < 5 {
		fpHeight = 5
	}

	fp := filepicker.New()
	fp.CurrentDirectory = cwd
	fp.DirAllowed = false
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.ShowPermissions = false
	fp.ShowSize = false
	fp.AutoHeight = false
	fp.SetHeight(fpHeight)

	pi := textinput.New()
	pi.Prompt = "  Path: "
	pi.Placeholder = "/path/to/game/build"
	pi.CharLimit = 512
	pi.Focus()

	prog := progress.New(progress.WithWidth(progressWidth(width)))

	return Model{
		stage:      stageTypeSelect,
		team:       team,
		game:       game,
		client:     client,
		typeOpts:   []string{"Math", "Front-end"},
		pathInput:  pi,
		filePicker: fp,
		spinner:    spinner.New(spinner.WithSpinner(spinner.Dot)),
		progress:   prog,
		viewport:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		width:      width,
		height:     height,
	}
}

func progressWidth(termWidth int) int {
	w := termWidth - 6
	if w < 20 {
		w = 20
	}
	if w > 80 {
		w = 80
	}
	return w
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = ws.Width
		m.height = ws.Height
		fpHeight := ws.Height - 8
		if fpHeight < 5 {
			fpHeight = 5
		}
		m.filePicker.SetHeight(fpHeight)
		m.filePicker, _ = m.filePicker.Update(msg)
		m.progress.SetWidth(progressWidth(ws.Width))
	}

	switch m.stage {
	case stageTypeSelect:
		return m.updateTypeSelect(msg)
	case stagePathInput:
		return m.updatePathInput(msg)
	case stageFilePicker:
		return m.updateFilePicker(msg)
	case stageScanning:
		return m.updateScanning(msg)
	case stageSafetyWarn:
		return m.updateSafetyWarn(msg)
	case stageCompliance:
		return m.updateCompliance(msg)
	case stageConfirm:
		return m.updateConfirm(msg)
	case stageUploading:
		return m.updateUploading(msg)
	case stageDone:
		return m.updateDone(msg)
	}
	return m, nil
}

func (m Model) View() string {
	switch m.stage {
	case stageTypeSelect:
		return m.viewTypeSelect()
	case stagePathInput:
		return m.viewPathInput()
	case stageFilePicker:
		return m.viewFilePicker()
	case stageScanning:
		return m.viewScanning()
	case stageSafetyWarn:
		return m.viewSafetyWarn()
	case stageCompliance:
		return m.viewCompliance()
	case stageConfirm:
		return m.viewConfirm()
	case stageUploading:
		return m.viewUploading()
	case stageDone:
		return m.viewDone()
	}
	return ""
}

// --- Type Select ---

func (m Model) updateTypeSelect(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "q", "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "up", "k":
			if m.typeCursor > 0 {
				m.typeCursor--
			}
		case "down", "j":
			if m.typeCursor < len(m.typeOpts)-1 {
				m.typeCursor++
			}
		case "enter":
			if m.typeCursor == 0 {
				m.uploadType = "math"
			} else {
				m.uploadType = "front"
			}
			m.pathErr = ""
			m.stage = stagePathInput
			m.pathInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m Model) viewTypeSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Upload to %s / %s\n\n", m.team, m.game)
	b.WriteString("  Select upload type:\n\n")
	for i, opt := range m.typeOpts {
		cursor := "  "
		if i == m.typeCursor {
			cursor = "> "
		}
		fmt.Fprintf(&b, "  %s%s\n", cursor, opt)
	}
	b.WriteString("\n  j/k: navigate • Enter: select • q: back\n")
	return b.String()
}

// --- Path Input ---

func (m Model) updatePathInput(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.stage = stageTypeSelect
			return m, nil
		case "tab":
			m.stage = stageFilePicker
			return m, m.filePicker.Init()
		case "down":
			if m.pathFocus == 0 {
				m.pathFocus = 1
				m.pathInput.Blur()
				return m, nil
			}
		case "up":
			if m.pathFocus == 1 {
				m.pathFocus = 0
				m.pathInput.Focus()
				return m, textinput.Blink
			}
		case "enter":
			if m.pathFocus == 1 {
				m.stage = stageFilePicker
				return m, m.filePicker.Init()
			}
			path := strings.TrimSpace(m.pathInput.Value())
			if path == "" {
				m.pathErr = "Path cannot be empty"
				return m, nil
			}
			info, err := os.Stat(path)
			if err != nil {
				m.pathErr = fmt.Sprintf("Path not found: %s", path)
				return m, nil
			}
			if !info.IsDir() {
				m.pathErr = "Path must be a directory, not a file"
				return m, nil
			}
			m.dirPath = path
			m.pathErr = ""
			m.stage = stageScanning
			m.scanPhase = "Scanning"
			return m, tea.Batch(m.spinner.Tick, m.doScan())
		}
	}

	if m.pathFocus == 0 {
		var cmd tea.Cmd
		m.pathInput, cmd = m.pathInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) viewPathInput() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Upload %s to %s / %s\n\n", m.uploadType, m.team, m.game)
	b.WriteString("  Enter path to directory:\n\n")
	b.WriteString(m.pathInput.View())
	b.WriteString("\n")

	if m.pathErr != "" {
		fmt.Fprintf(&b, "\n  ✗ %s\n", m.pathErr)
	}

	b.WriteString("\n")
	if m.pathFocus == 1 {
		b.WriteString("  > Browse with interactive picker...\n")
	} else {
		b.WriteString("    Browse with interactive picker...\n")
	}

	b.WriteString("\n  Enter: confirm • ↑/↓: switch • Esc: back\n")
	return b.String()
}

// --- File Picker ---

func (m Model) updateFilePicker(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "q":
			m.stage = stagePathInput
			m.pathFocus = 0
			m.pathInput.Focus()
			return m, textinput.Blink
		case "space", " ":
			dir := m.filePicker.CurrentDirectory
			m.dirPath = dir
			m.pathInput.SetValue(dir)
			m.stage = stageScanning
			m.scanPhase = "Scanning"
			return m, tea.Batch(m.spinner.Tick, m.doScan())
		}
	}

	var cmd tea.Cmd
	m.filePicker, cmd = m.filePicker.Update(msg)
	return m, cmd
}

func (m Model) viewFilePicker() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Upload %s to %s / %s\n", m.uploadType, m.team, m.game)
	fmt.Fprintf(&b, "  Current: %s\n\n", m.filePicker.CurrentDirectory)
	b.WriteString(m.filePicker.View())
	b.WriteString("\n  Enter/l: open dir • h/←/Esc: parent dir • Space: select this dir • q: back\n")
	return b.String()
}

// --- Scanning ---

func (m Model) doScan() tea.Cmd {
	dirPath := m.dirPath
	return func() tea.Msg {
		warnings := upload.ValidatePath(dirPath)
		if upload.HasErrors(warnings) {
			return scanDoneMsg{warnings: warnings}
		}
		files, err := upload.ScanDirectory(dirPath, "")
		return scanDoneMsg{files: files, warnings: warnings, err: err}
	}
}

func (m Model) updateScanning(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case scanDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.stage = stageDone
			m.resultMsg = fmt.Sprintf("Scan failed: %v", msg.err)
			return m, nil
		}
		m.localFiles = msg.files
		m.warnings = msg.warnings

		if upload.HasErrors(msg.warnings) || len(msg.warnings) > 0 {
			m.stage = stageSafetyWarn
			return m, nil
		}

		return m.afterSafetyCheck()
	case complianceDoneMsg:
		m.compliance = msg.result
		m.compliancePage = 0
		m.stage = stageCompliance
		return m, nil
	case planDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.stage = stageDone
			m.resultMsg = fmt.Sprintf("Planning failed: %v", msg.err)
			return m, nil
		}
		m.plan = msg.plan
		m.stage = stageConfirm
		m.viewport.SetContent(m.buildConfirmContent())
		return m, nil
	}
	return m, nil
}

func (m Model) viewScanning() string {
	return fmt.Sprintf("\n  %s %s %s...\n", m.spinner.View(), m.scanPhase, m.dirPath)
}

// --- Safety Warnings ---

func (m Model) updateSafetyWarn(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "y":
			if !upload.HasErrors(m.warnings) {
				return m.afterSafetyCheck()
			}
		case "n", "q", "esc":
			m.stage = stagePathInput
			m.pathFocus = 0
			m.pathInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m Model) viewSafetyWarn() string {
	var b strings.Builder
	b.WriteString("\n  Safety check results:\n\n")
	for _, w := range m.warnings {
		prefix := "  ⚠ "
		if w.Level == "error" {
			prefix = "  ✗ "
		}
		fmt.Fprintf(&b, "  %s%s\n", prefix, w.Message)
	}

	if upload.HasErrors(m.warnings) {
		b.WriteString("\n  Cannot proceed. Press q to go back.\n")
	} else {
		b.WriteString("\n  y: continue anyway • n: go back\n")
	}
	return b.String()
}

// --- Compliance / Planning / Confirm ---

func (m Model) afterSafetyCheck() (Model, tea.Cmd) {
	if m.uploadType == "math" {
		return m.startCompliance()
	}
	return m.startPlanning()
}

func (m Model) startCompliance() (Model, tea.Cmd) {
	m.stage = stageScanning
	m.scanPhase = "Checking math compliance"
	dirPath := m.dirPath
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		result := upload.RunMathCompliance(dirPath)
		return complianceDoneMsg{result: result}
	})
}

func (m Model) updateCompliance(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		totalPages := complianceTotalPages(len(m.compliance.ModeSummaries))
		hasIssues := m.compliance.FailuresCount > 0 || m.compliance.WarningsCount > 0
		switch key.String() {
		case "enter":
			return m.startPlanning()
		case "s":
			if hasIssues {
				return m.startPlanning()
			}
			return m, nil
		case "r":
			m.stage = stageScanning
			m.scanPhase = "Rescanning"
			return m, tea.Batch(m.spinner.Tick, m.doScan())
		case "down", "j", "right", "l":
			if totalPages > 1 && m.compliancePage < totalPages-1 {
				m.compliancePage++
			}
			return m, nil
		case "up", "k", "left", "h":
			if totalPages > 1 && m.compliancePage > 0 {
				m.compliancePage--
			}
			return m, nil
		case "q", "esc":
			m.stage = stagePathInput
			m.pathFocus = 0
			m.pathInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m Model) viewCompliance() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Math Compliance Check — %s/%s\n\n", m.team, m.game)
	failCount := m.compliance.FailuresCount
	warnCount := m.compliance.WarningsCount

	fmt.Fprintf(&b, "  Modes: %d • Failures: %d • Warnings: %d\n\n",
		len(m.compliance.ModeSummaries), failCount, warnCount)

	rows := m.compliance.ModeSummaries
	start, end, page, totalPages := compliancePageRange(len(rows), m.compliancePage)
	pageRows := rows[start:end]

	if len(pageRows) > 0 {
		cols := complianceColumns()
		available := m.width - 4
		if available < 40 {
			available = 40
		}
		for len(cols) > 1 && complianceTableWidth(cols) > available {
			cols = cols[:len(cols)-1]
		}

		b.WriteString("  " + complianceRenderHeader(cols) + "\n")
		b.WriteString("  " + strings.Repeat("-", len(complianceRenderHeader(cols))) + "\n")
		for _, r := range pageRows {
			b.WriteString("  " + complianceRenderRow(cols, r) + "\n")
		}
	} else {
		b.WriteString("  No mode summary available.\n")
	}

	if totalPages > 1 {
		fmt.Fprintf(&b, "\n  Page %d/%d • j/k: next/prev page\n", page+1, totalPages)
	}

	b.WriteString("\n")
	if failCount > 0 || warnCount > 0 {
		b.WriteString("  Enter: continue • s: proceed anyway • r: reload • q: go back\n")
	} else {
		b.WriteString("  Enter: continue • r: reload • q: go back\n")
	}

	return b.String()
}

type complianceColumn struct {
	Title string
	Width int
	Value func(upload.ComplianceModeSummary) string
}

func complianceColumns() []complianceColumn {
	return []complianceColumn{
		{Title: "Name", Width: 16, Value: func(m upload.ComplianceModeSummary) string { return m.Name }},
		{Title: "Cost", Width: 8, Value: func(m upload.ComplianceModeSummary) string { return fmt.Sprintf("%.2f", m.Cost) }},
		{Title: "RTP", Width: 8, Value: func(m upload.ComplianceModeSummary) string { return formatMetricPercent(m.RTP, m.HasStats) }},
		{Title: "Volatility", Width: 18, Value: func(m upload.ComplianceModeSummary) string { return formatVolatility(m) }},
		{Title: "HitRate", Width: 9, Value: func(m upload.ComplianceModeSummary) string { return formatMetricPercent(m.HitRate, m.HasStats) }},
		{Title: "MaxWin", Width: 9, Value: func(m upload.ComplianceModeSummary) string { return formatMetricX(m.MaxWin, m.HasStats) }},
		{Title: "MaxWinHR", Width: 12, Value: func(m upload.ComplianceModeSummary) string { return formatOddsCell(m.MaxWinHitRate, m.HasStats) }},
		{Title: "SimCount", Width: 10, Value: func(m upload.ComplianceModeSummary) string { return formatSimCount(m.SimCount, m.HasStats) }},
	}
}

func complianceTableWidth(cols []complianceColumn) int {
	if len(cols) == 0 {
		return 0
	}
	width := 0
	for i, c := range cols {
		width += c.Width
		if i > 0 {
			width += 3
		}
	}
	return width
}

func complianceRenderHeader(cols []complianceColumn) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, padAndTrim(c.Title, c.Width))
	}
	return strings.Join(parts, " | ")
}

func complianceRenderRow(cols []complianceColumn, row upload.ComplianceModeSummary) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, padAndTrim(c.Value(row), c.Width))
	}
	return strings.Join(parts, " | ")
}

func complianceTotalPages(totalRows int) int {
	if totalRows <= 0 {
		return 1
	}
	pages := totalRows / 10
	if totalRows%10 != 0 {
		pages++
	}
	return pages
}

func compliancePageRange(totalRows, page int) (start int, end int, safePage int, totalPages int) {
	totalPages = complianceTotalPages(totalRows)
	safePage = page
	if safePage < 0 {
		safePage = 0
	}
	if safePage >= totalPages {
		safePage = totalPages - 1
	}
	start = safePage * 10
	end = start + 10
	if end > totalRows {
		end = totalRows
	}
	if start > end {
		start = end
	}
	return start, end, safePage, totalPages
}

func padAndTrim(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(s) > width {
		if width == 1 {
			return "…"
		}
		return s[:width-1] + "…"
	}
	return s + strings.Repeat(" ", width-len(s))
}

func formatMetricPercent(v float64, hasStats bool) string {
	if !hasStats {
		return "-"
	}
	return fmt.Sprintf("%.2f%%", v)
}

func formatMetricX(v float64, hasStats bool) string {
	if !hasStats {
		return "-"
	}
	return fmt.Sprintf("%.2fx", v)
}

func formatSimCount(v int, hasStats bool) string {
	if !hasStats {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}

func formatOddsCell(v string, hasStats bool) string {
	if !hasStats || v == "" {
		return "-"
	}
	return v
}

func formatVolatility(m upload.ComplianceModeSummary) string {
	if !m.HasStats {
		return "-"
	}
	return fmt.Sprintf("%s(%.2fσ)", shortVolTag(m.VolatilityTag), m.Volatility)
}

func shortVolTag(tag string) string {
	switch tag {
	case "Very Low":
		return "VLow"
	case "Low":
		return "Low"
	case "Medium-Low":
		return "Med-Low"
	case "Medium":
		return "Med"
	case "Medium-High":
		return "Med-High"
	case "High":
		return "High"
	case "Very High":
		return "VHigh"
	case "Extreme":
		return "Extreme"
	default:
		return tag
	}
}

func (m Model) startPlanning() (Model, tea.Cmd) {
	m.stage = stageScanning
	m.scanPhase = "Planning upload"
	team, game, uploadType, dirPath := m.team, m.game, m.uploadType, m.dirPath
	client := m.client
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		u := upload.NewUploader(client)
		plan, err := u.Plan(context.Background(), team, game, uploadType, dirPath)
		return planDoneMsg{plan: plan, err: err}
	})
}

func (m Model) updateConfirm(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y":
			return m.startUpload()
		case "n", "q", "esc":
			m.stage = stagePathInput
			m.pathFocus = 0
			m.pathInput.Focus()
			return m, textinput.Blink
		case "up", "k":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "down", "j":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	case planDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.stage = stageDone
			m.resultMsg = fmt.Sprintf("Planning failed: %v", msg.err)
			return m, nil
		}
		m.plan = msg.plan
		m.stage = stageConfirm
		m.viewport.SetContent(m.buildConfirmContent())
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m Model) buildConfirmContent() string {
	var b strings.Builder
	plan := m.plan

	if len(plan.ToUpload) > 0 {
		fmt.Fprintf(&b, "  New files:       %d (%s)\n", len(plan.ToUpload), upload.FormatSize(plan.TotalUploadBytes()))
	}
	if len(plan.ToCopy) > 0 {
		fmt.Fprintf(&b, "  Moved files:     %d\n", len(plan.ToCopy))
	}
	if len(plan.ToDelete) > 0 {
		fmt.Fprintf(&b, "  Removed files:   %d\n", len(plan.ToDelete))
	}
	if len(plan.Unchanged) > 0 {
		fmt.Fprintf(&b, "  Unchanged:       %d\n", len(plan.Unchanged))
	}

	fmt.Fprintf(&b, "\n  Total upload size: %s\n", upload.FormatSize(plan.TotalUploadBytes()))
	fmt.Fprintf(&b, "  Total actions:     %d\n", plan.TotalActions())

	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Publish %s to %s / %s\n\n", m.uploadType, m.team, m.game)
	b.WriteString(m.viewport.View())
	b.WriteString("\n\n  y: confirm upload • n: cancel • j/k: scroll\n")
	return b.String()
}

// --- Upload ---

func waitForUploadProgress(ch <-chan upload.ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return uploadFinishedMsg{}
		}
		return uploadProgressMsg{evt}
	}
}

func bytesTickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
		return bytesTickMsg{}
	})
}

func (m Model) startUpload() (Model, tea.Cmd) {
	m.stage = stageUploading
	m.progressTotal = m.plan.TotalActions()
	m.progressCurrent = 0
	m.progressFile = ""
	m.progressErrors = nil
	m.activeFiles = nil
	m.startTime = time.Now()
	m.bytesTotal = m.plan.TotalUploadBytes()
	m.byteCounter = new(atomic.Int64)
	m.uploadSucceeded = false
	m.doneCursor = 0
	m.publishInFlight = false
	m.published = false
	m.publishMsg = ""
	m.publishErr = nil

	ctx, cancel := context.WithCancel(context.Background())
	m.uploadCancel = cancel

	gate := upload.NewPauseGate()
	m.uploadGate = gate

	ch := make(chan upload.ProgressEvent, 64)
	m.uploadCh = ch

	plan := m.plan
	client := m.client
	counter := m.byteCounter

	go func() {
		defer close(ch)
		u := upload.NewUploader(client)
		u.ByteCounter = counter
		_ = u.Execute(ctx, plan, gate, func(evt upload.ProgressEvent) {
			select {
			case ch <- evt:
			case <-ctx.Done():
			}
		})
	}()

	return m, tea.Batch(m.spinner.Tick, waitForUploadProgress(ch), bytesTickCmd())
}

func (m Model) updateUploading(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "c":
			if m.uploadCancel != nil {
				m.uploadCancel()
			}
			return m, nil
		case "p":
			if m.uploadGate != nil {
				m.uploadGate.Toggle()
			}
			return m, nil
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd
	case bytesTickMsg:
		if m.stage != stageUploading {
			return m, nil
		}
		if m.byteCounter != nil {
			sent := m.byteCounter.Load()
			percent := 0.0
			if m.bytesTotal > 0 {
				percent = float64(sent) / float64(m.bytesTotal)
				if percent > 1.0 {
					percent = 1.0
				}
			}
			cmd := m.progress.SetPercent(percent)
			return m, tea.Batch(cmd, bytesTickCmd())
		}
		return m, bytesTickCmd()
	case uploadProgressMsg:
		if msg.Phase == "start" {
			m.activeFiles = append(m.activeFiles, activeFile{name: msg.FileName, size: msg.FileSize})
			return m, waitForUploadProgress(m.uploadCh)
		}
		// Remove completed file from active list
		for i, af := range m.activeFiles {
			if af.name == msg.FileName {
				m.activeFiles = append(m.activeFiles[:i], m.activeFiles[i+1:]...)
				break
			}
		}
		m.progressCurrent = msg.Current
		m.progressFile = msg.FileName
		if msg.Error != nil {
			m.progressErrors = append(m.progressErrors, fmt.Sprintf("%s: %v", msg.FileName, msg.Error))
		}
		return m, waitForUploadProgress(m.uploadCh)
	case uploadFinishedMsg:
		m.stage = stageDone
		m.doneCursor = 0
		m.publishInFlight = false
		m.publishMsg = ""
		m.publishErr = nil
		elapsed := time.Since(m.startTime)
		if len(m.progressErrors) > 0 {
			m.err = fmt.Errorf("%d errors during upload", len(m.progressErrors))
			m.resultMsg = fmt.Sprintf("Upload completed with %d errors in %s", len(m.progressErrors), elapsed.Round(time.Second))
			m.uploadSucceeded = false
		} else if m.progressCurrent < m.progressTotal {
			m.resultMsg = "Upload cancelled"
			m.uploadSucceeded = false
		} else {
			m.resultMsg = fmt.Sprintf("Upload complete! %d files synced in %s",
				m.plan.TotalActions(), elapsed.Round(time.Second))
			m.uploadSucceeded = true
		}
		return m, nil
	}
	return m, nil
}

func (m Model) viewUploading() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Uploading to %s/%s (%s)\n\n", m.team, m.game, m.uploadType)

	b.WriteString("  ")
	b.WriteString(m.progress.View())
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %d / %d files\n\n", m.progressCurrent, m.progressTotal)

	if len(m.activeFiles) > 0 {
		sorted := make([]activeFile, len(m.activeFiles))
		copy(sorted, m.activeFiles)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
		for _, af := range sorted {
			fmt.Fprintf(&b, "  %s ↑ %s (%s)\n", m.spinner.View(), af.name, upload.FormatSize(af.size))
		}
		b.WriteString("\n")
	} else if m.progressFile != "" {
		fmt.Fprintf(&b, "  %s %s\n\n", m.spinner.View(), m.progressFile)
	}

	var bytesUploaded int64
	if m.byteCounter != nil {
		bytesUploaded = m.byteCounter.Load()
	}

	elapsed := time.Since(m.startTime)
	if bytesUploaded > 0 && elapsed.Seconds() > 0.5 {
		speed := float64(bytesUploaded) / elapsed.Seconds()
		fmt.Fprintf(&b, "  Speed: %s/s", upload.FormatSize(int64(speed)))
		remaining := m.bytesTotal - bytesUploaded
		if remaining > 0 && speed > 0 {
			eta := time.Duration(float64(remaining)/speed) * time.Second
			fmt.Fprintf(&b, " • ETA: %s", eta.Round(time.Second))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  Transferred: %s / %s\n",
		upload.FormatSize(bytesUploaded), upload.FormatSize(m.bytesTotal))

	if m.uploadGate != nil && m.uploadGate.IsPaused() {
		b.WriteString("\n  ⏸  PAUSED\n")
	}

	if len(m.progressErrors) > 0 {
		fmt.Fprintf(&b, "\n  Errors (%d):\n", len(m.progressErrors))
		show := m.progressErrors
		if len(show) > 3 {
			show = show[len(show)-3:]
		}
		for _, e := range show {
			fmt.Fprintf(&b, "    ✗ %s\n", e)
		}
	}

	b.WriteString("\n  p: pause/resume • c: cancel\n")
	return b.String()
}

// --- Done ---

func (m Model) updateDone(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.publishInFlight {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	case publishDoneMsg:
		m.publishInFlight = false
		if msg.err != nil {
			m.publishErr = msg.err
			m.publishMsg = ""
			return m, nil
		}
		if msg.result != nil && msg.result.IsError() {
			m.publishErr = fmt.Errorf("publish %s error [%s]: %s", m.uploadType, msg.result.Code, msg.result.Error())
			m.publishMsg = ""
			return m, nil
		}
		if msg.result != nil {
			m.publishErr = nil
			m.published = true
			m.doneCursor = 0
			m.publishMsg = fmt.Sprintf("Published %s v%d (changed: %v)", m.uploadType, msg.result.Version, msg.result.Changed)
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.publishInFlight {
			return m, nil
		}
		canPublish := m.uploadSucceeded && !m.published
		switch msg.String() {
		case "q", "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "up", "k":
			if m.uploadSucceeded {
				m.doneCursor = 0
				return m, nil
			}
		case "down", "j":
			if canPublish {
				m.doneCursor = 1
				return m, nil
			}
		case "enter":
			if !canPublish || m.doneCursor == 0 {
				return m, func() tea.Msg { return GoBackMsg{} }
			}
			m.publishInFlight = true
			m.publishErr = nil
			m.publishMsg = ""
			return m, tea.Batch(m.spinner.Tick, m.doPublish())
		}
	}
	return m, nil
}

func (m Model) viewDone() string {
	icon := "✓"
	if m.err != nil {
		icon = "✗"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n", icon, m.resultMsg)

	if len(m.progressErrors) > 0 {
		fmt.Fprintf(&b, "\n  Errors (%d):\n", len(m.progressErrors))
		for _, e := range m.progressErrors {
			fmt.Fprintf(&b, "    ✗ %s\n", e)
		}
	}

	if m.publishInFlight {
		fmt.Fprintf(&b, "\n  %s Publishing %s...\n", m.spinner.View(), m.uploadType)
		b.WriteString("\n  Please wait...\n")
		return b.String()
	}

	if m.publishErr != nil {
		fmt.Fprintf(&b, "\n  ✗ Publish failed: %v\n", m.publishErr)
	}
	if m.publishMsg != "" {
		fmt.Fprintf(&b, "\n  ✓ %s\n", m.publishMsg)
	}

	if m.uploadSucceeded {
		backCursor := "  "
		canPublish := !m.published
		publishCursor := "  "
		if m.doneCursor == 0 {
			backCursor = "> "
		} else if canPublish {
			publishCursor = "> "
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %sBack\n", backCursor)
		if canPublish {
			fmt.Fprintf(&b, "  %sPublish %s\n", publishCursor, m.uploadType)
			b.WriteString("\n  ↑/↓: select • Enter: confirm • q: back\n")
		} else {
			b.WriteString("\n  Enter: back • q: back\n")
		}
		return b.String()
	}

	b.WriteString("\n  Press Enter to go back.\n")
	return b.String()
}

func (m Model) doPublish() tea.Cmd {
	team, game, uploadType := m.team, m.game, m.uploadType
	client := m.client
	return func() tea.Msg {
		result, err := client.Publish(context.Background(), team, game, uploadType)
		return publishDoneMsg{result: result, err: err}
	}
}

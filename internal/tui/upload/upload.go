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
	stageConfirm
	stageSafetyWarn
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

	localFiles []upload.LocalFile
	warnings   []upload.SafetyWarning
	plan       *upload.UploadPlan

	// Upload state
	uploadCh       <-chan upload.ProgressEvent
	uploadCancel   context.CancelFunc
	uploadGate     *upload.PauseGate
	byteCounter    *atomic.Int64
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
	b.WriteString(fmt.Sprintf("\n  Upload to %s / %s\n\n", m.team, m.game))
	b.WriteString("  Select upload type:\n\n")
	for i, opt := range m.typeOpts {
		cursor := "  "
		if i == m.typeCursor {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("  %s%s\n", cursor, opt))
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
	b.WriteString(fmt.Sprintf("\n  Upload %s to %s / %s\n\n", m.uploadType, m.team, m.game))
	b.WriteString("  Enter path to directory:\n\n")
	b.WriteString(m.pathInput.View())
	b.WriteString("\n")

	if m.pathErr != "" {
		b.WriteString(fmt.Sprintf("\n  ✗ %s\n", m.pathErr))
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
		case " ":
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
	b.WriteString(fmt.Sprintf("\n  Upload %s to %s / %s\n", m.uploadType, m.team, m.game))
	b.WriteString(fmt.Sprintf("  Current: %s\n\n", m.filePicker.CurrentDirectory))
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

		return m.startPlanning()
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
				return m.startPlanning()
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
		b.WriteString(fmt.Sprintf("  %s%s\n", prefix, w.Message))
	}

	if upload.HasErrors(m.warnings) {
		b.WriteString("\n  Cannot proceed. Press q to go back.\n")
	} else {
		b.WriteString("\n  y: continue anyway • n: go back\n")
	}
	return b.String()
}

// --- Planning / Confirm ---

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
		b.WriteString(fmt.Sprintf("  Upload (%d files):\n", len(plan.ToUpload)))
		for _, e := range plan.ToUpload {
			b.WriteString(fmt.Sprintf("    + %s (%s)\n", e.RemoteKey, upload.FormatSize(e.Size)))
		}
		b.WriteString("\n")
	}

	if len(plan.ToCopy) > 0 {
		b.WriteString(fmt.Sprintf("  Copy (%d files):\n", len(plan.ToCopy)))
		for _, e := range plan.ToCopy {
			b.WriteString(fmt.Sprintf("    ~ %s (%s)\n", e.RemoteKey, upload.FormatSize(e.Size)))
		}
		b.WriteString("\n")
	}

	if len(plan.ToDelete) > 0 {
		b.WriteString(fmt.Sprintf("  Delete (%d files):\n", len(plan.ToDelete)))
		for _, e := range plan.ToDelete {
			b.WriteString(fmt.Sprintf("    - %s\n", e.RemoteKey))
		}
		b.WriteString("\n")
	}

	if len(plan.Unchanged) > 0 {
		b.WriteString(fmt.Sprintf("  Unchanged: %d files\n\n", len(plan.Unchanged)))
	}

	b.WriteString(fmt.Sprintf("  Total upload size: %s\n", upload.FormatSize(plan.TotalUploadBytes())))
	b.WriteString(fmt.Sprintf("  Total actions: %d\n", plan.TotalActions()))

	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n  Upload plan: %s → %s/%s (%s)\n\n", m.dirPath, m.team, m.game, m.uploadType))
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
		elapsed := time.Since(m.startTime)
		if len(m.progressErrors) > 0 {
			m.err = fmt.Errorf("%d errors during upload", len(m.progressErrors))
			m.resultMsg = fmt.Sprintf("Upload completed with %d errors in %s", len(m.progressErrors), elapsed.Round(time.Second))
		} else if m.progressCurrent < m.progressTotal {
			m.resultMsg = "Upload cancelled"
		} else {
			m.resultMsg = fmt.Sprintf("Upload complete! %d files synced in %s",
				m.plan.TotalActions(), elapsed.Round(time.Second))
		}
		return m, nil
	}
	return m, nil
}

func (m Model) viewUploading() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n  Uploading to %s/%s (%s)\n\n", m.team, m.game, m.uploadType))

	b.WriteString("  ")
	b.WriteString(m.progress.View())
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  %d / %d files\n\n", m.progressCurrent, m.progressTotal))

	if len(m.activeFiles) > 0 {
		sorted := make([]activeFile, len(m.activeFiles))
		copy(sorted, m.activeFiles)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
		for _, af := range sorted {
			b.WriteString(fmt.Sprintf("  %s ↑ %s (%s)\n", m.spinner.View(), af.name, upload.FormatSize(af.size)))
		}
		b.WriteString("\n")
	} else if m.progressFile != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n\n", m.spinner.View(), m.progressFile))
	}

	var bytesUploaded int64
	if m.byteCounter != nil {
		bytesUploaded = m.byteCounter.Load()
	}

	elapsed := time.Since(m.startTime)
	if bytesUploaded > 0 && elapsed.Seconds() > 0.5 {
		speed := float64(bytesUploaded) / elapsed.Seconds()
		b.WriteString(fmt.Sprintf("  Speed: %s/s", upload.FormatSize(int64(speed))))
		remaining := m.bytesTotal - bytesUploaded
		if remaining > 0 && speed > 0 {
			eta := time.Duration(float64(remaining)/speed) * time.Second
			b.WriteString(fmt.Sprintf(" • ETA: %s", eta.Round(time.Second)))
		}
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("  Transferred: %s / %s\n",
		upload.FormatSize(bytesUploaded), upload.FormatSize(m.bytesTotal)))

	if m.uploadGate != nil && m.uploadGate.IsPaused() {
		b.WriteString("\n  ⏸  PAUSED\n")
	}

	if len(m.progressErrors) > 0 {
		b.WriteString(fmt.Sprintf("\n  Errors (%d):\n", len(m.progressErrors)))
		show := m.progressErrors
		if len(show) > 3 {
			show = show[len(show)-3:]
		}
		for _, e := range show {
			b.WriteString(fmt.Sprintf("    ✗ %s\n", e))
		}
	}

	b.WriteString("\n  p: pause/resume • c: cancel\n")
	return b.String()
}

// --- Done ---

func (m Model) updateDone(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter", "q", "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
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
	b.WriteString(fmt.Sprintf("\n  %s %s\n", icon, m.resultMsg))

	if len(m.progressErrors) > 0 {
		b.WriteString(fmt.Sprintf("\n  Errors (%d):\n", len(m.progressErrors)))
		for _, e := range m.progressErrors {
			b.WriteString(fmt.Sprintf("    ✗ %s\n", e))
		}
	}

	b.WriteString("\n  Press Enter to go back.\n")
	return b.String()
}

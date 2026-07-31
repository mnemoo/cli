package upload

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/repack"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/upload"
)

type stage int

const (
	stageTypeSelect stage = iota
	stagePathInput
	stageFilePicker
	stageScanning
	stageSafetyWarn
	stageRepackOffer
	stageCompliance
	stageUploadRoute
	stageAcceleratorForm
	stageAcceleratorBusy
	stageAcceleratorReady
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
	plan          *upload.UploadPlan
	approvalKnown bool
	approvalOpen  bool
	approvalErr   error
	err           error
}

type throughputSample struct {
	at    time.Time
	bytes int64
}

type uploadProgressMsg struct {
	upload.ProgressEvent
}

type uploadFinishedMsg struct{}

type bytesTickMsg struct{}

type complianceDoneMsg struct {
	result upload.ComplianceResult
}

type repackEstimateDoneMsg struct {
	report repack.Report
	err    error
}

type repackApplyDoneMsg struct {
	report  repack.ApplyReport
	cleanup func()
	err     error
}

type repackProgressMsg struct {
	progress repack.Progress
}

type acceleratorConnectedMsg struct {
	client      *accelerator.Client
	profileName string
	address     string
	fingerprint string
	newProfile  bool
	err         error
}

type acceleratorSpeedTestDoneMsg struct {
	result accelerator.SpeedTestResult
	err    error
}

type publishDoneMsg struct {
	result         *stake.PublishResult
	approvalUpdate *stake.ApprovalVersionUpdate
	approvalErr    error
	err            error
}

type Model struct {
	stage         stage
	team          string
	game          string
	uploadType    string
	dirPath       string
	uploadPath    string
	service       *service.Service
	directService *service.Service

	typeCursor int
	typeOpts   []string

	pathInput  textinput.Model
	pathErr    string
	pathFocus  int // 0 = text input, 1 = browse button
	filePicker filepicker.Model
	spinner    spinner.Model
	progress   progress.Model
	viewport   viewport.Model

	acceleratorProfiles        *accelerator.Profiles
	acceleratorProfileNames    []string
	acceleratorRouteCursor     int
	acceleratorFormFocus       int
	acceleratorAdding          bool
	acceleratorSelectedName    string
	acceleratorSelectedAddr    string
	acceleratorAddressInput    textinput.Model
	acceleratorNameInput       textinput.Model
	acceleratorPasswordInput   textinput.Model
	acceleratorMakeDefault     bool
	acceleratorClient          *accelerator.Client
	acceleratorFingerprint     string
	acceleratorNewlyPinned     bool
	acceleratorReadyCursor     int
	acceleratorBusyLabel       string
	acceleratorBusyReturn      stage
	acceleratorCancel          context.CancelFunc
	acceleratorErr             error
	acceleratorSpeedResult     *accelerator.SpeedTestResult
	acceleratorSpeedErr        error
	uploadRouteLabel           string
	connectExistingAccelerator func(context.Context, string, string) (*accelerator.Client, string, error)
	connectNewAccelerator      func(context.Context, string, string, string, bool) (*accelerator.Client, string, error)
	runAcceleratorSpeedTest    func(context.Context, *accelerator.Client, int64) (accelerator.SpeedTestResult, error)

	localFiles         []upload.LocalFile
	warnings           []upload.SafetyWarning
	plan               *upload.UploadPlan
	compliance         upload.ComplianceResult
	complianceOptions  upload.ComplianceOptions
	complianceOverride bool
	complianceConfirm  bool
	compliancePage     int
	repackReport       repack.Report
	repackEstimated    bool
	repackEstimateErr  error
	repackOfferHandled bool
	repackApplyReport  repack.ApplyReport
	repackApplyErr     error
	repackApplyDone    bool
	repackCleanup      func()
	repackCancel       context.CancelFunc
	repackProgress     repack.Progress
	repackProgressCh   <-chan tea.Msg
	repackStartedAt    time.Time
	confirmCursor      int
	publishAfterUpload bool
	changeApproval     bool
	forceApproval      bool
	forcePublish       bool
	preferences        uploadPreferences
	preferencesPath    string
	preferencesErr     error
	approvalKnown      bool
	approvalOpen       bool
	approvalErr        error
	prefilled          bool
	doneCursor         int
	uploadSucceeded    bool
	publishInFlight    bool
	published          bool
	publishMsg         string
	publishErr         error

	// Upload state
	uploadCh        <-chan upload.ProgressEvent
	uploadCancel    context.CancelFunc
	uploadGate      *upload.PauseGate
	transferTracker *upload.TransferTracker
	transferState   upload.TransferSnapshot
	rateSamples     []throughputSample
	fileRateSamples map[string][]throughputSample
	fileRates       map[string]float64
	overallRate     float64
	uploadRunErr    error
	uploadScroll    int
	errorLogPath    string
	progressCurrent int
	progressTotal   int
	progressFile    string
	progressErrors  []string
	startTime       time.Time
	bytesTotal      int64

	width     int
	height    int
	scanPhase string
	resultMsg string
	err       error
}

func New(appService *service.Service, team, game string, width, height int) Model {
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

	acceleratorAddress := textinput.New()
	acceleratorAddress.Placeholder = "203.0.113.10:7775"
	acceleratorAddress.CharLimit = 256
	acceleratorAddress.SetWidth(52)

	acceleratorName := textinput.New()
	acceleratorName.Placeholder = "auto-generated from address"
	acceleratorName.CharLimit = 64
	acceleratorName.SetWidth(52)

	acceleratorPassword := textinput.New()
	acceleratorPassword.Placeholder = "at least 12 characters"
	acceleratorPassword.CharLimit = 512
	acceleratorPassword.SetWidth(52)
	acceleratorPassword.EchoMode = textinput.EchoPassword
	acceleratorPassword.EchoCharacter = '•'

	prog := progress.New(progress.WithWidth(progressWidth(width)))
	preferences, preferencesPath, preferencesErr := loadUploadPreferences()

	return Model{
		stage:                      stageTypeSelect,
		team:                       team,
		game:                       game,
		service:                    appService,
		directService:              appService.Direct(),
		typeOpts:                   []string{"Math", "Front-end"},
		pathInput:                  pi,
		filePicker:                 fp,
		spinner:                    spinner.New(spinner.WithSpinner(spinner.Dot)),
		progress:                   prog,
		viewport:                   viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		acceleratorAddressInput:    acceleratorAddress,
		acceleratorNameInput:       acceleratorName,
		acceleratorPasswordInput:   acceleratorPassword,
		connectExistingAccelerator: accelerator.ConnectConfigured,
		connectNewAccelerator:      connectAndSaveAccelerator,
		runAcceleratorSpeedTest: func(ctx context.Context, client *accelerator.Client, size int64) (accelerator.SpeedTestResult, error) {
			return client.SpeedTest(ctx, size)
		},
		publishAfterUpload: preferences.PublishAfterUpload,
		changeApproval:     preferences.ChangeApprovalVersion,
		preferences:        preferences,
		preferencesPath:    preferencesPath,
		preferencesErr:     preferencesErr,
		width:              width,
		height:             height,
	}
}

// NewPublish creates a prefilled upload flow for `stakecli publish`.
func NewPublish(appService *service.Service, team, game, uploadType, path string, compliance upload.ComplianceOptions, updateApproval bool, width, height int) Model {
	m := New(appService, team, game, width, height)
	m.uploadType = uploadType
	m.dirPath = path
	m.complianceOptions = compliance
	m.pathInput.SetValue(path)
	m.stage = stageScanning
	m.scanPhase = "Scanning"
	m.publishAfterUpload = true
	m.changeApproval = updateApproval || m.preferences.ChangeApprovalVersion
	m.forceApproval = updateApproval
	m.forcePublish = true
	m.prefilled = true
	return m
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
	if m.prefilled {
		return tea.Batch(m.spinner.Tick, m.doScan())
	}
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
	case stageRepackOffer:
		return m.updateRepackOffer(msg)
	case stageCompliance:
		return m.updateCompliance(msg)
	case stageUploadRoute:
		return m.updateUploadRoute(msg)
	case stageAcceleratorForm:
		return m.updateAcceleratorForm(msg)
	case stageAcceleratorBusy:
		return m.updateAcceleratorBusy(msg)
	case stageAcceleratorReady:
		return m.updateAcceleratorReady(msg)
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
	case stageRepackOffer:
		return m.viewRepackOffer()
	case stageCompliance:
		return m.viewCompliance()
	case stageUploadRoute:
		return m.viewUploadRoute()
	case stageAcceleratorForm:
		return m.viewAcceleratorForm()
	case stageAcceleratorBusy:
		return m.viewAcceleratorBusy()
	case stageAcceleratorReady:
		return m.viewAcceleratorReady()
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
	case tea.KeyPressMsg:
		if m.repackCancel != nil && (msg.String() == "esc" || msg.String() == "q") {
			m.resetTemporaryRepack()
			m.stage = stageConfirm
			m.viewport.SetContent(m.buildConfirmContent())
			return m, nil
		}
		return m, nil
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
		m.resetTemporaryRepack()
		m.localFiles = msg.files
		m.warnings = msg.warnings
		m.repackReport = repack.Report{}
		m.repackEstimated = false
		m.repackEstimateErr = nil
		m.repackOfferHandled = false

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
	case repackEstimateDoneMsg:
		m.repackReport = msg.report
		m.repackEstimateErr = msg.err
		m.repackEstimated = true
		return m.startCompliance()
	case repackProgressMsg:
		m.repackProgress = msg.progress
		if m.repackProgressCh != nil {
			return m, waitForRepackProgress(m.repackProgressCh)
		}
		return m, nil
	case repackApplyDoneMsg:
		m.repackCancel = nil
		m.repackProgressCh = nil
		m.repackApplyDone = true
		m.repackApplyErr = msg.err
		if msg.err != nil {
			if msg.cleanup != nil {
				msg.cleanup()
			}
			m.stage = stageConfirm
			m.viewport.SetContent(m.buildConfirmContent())
			return m, nil
		}
		m.cleanupRepackFiles()
		m.repackApplyReport = msg.report
		if msg.report.AppliedFiles == 0 || !msg.report.TemporaryStaging {
			if msg.cleanup != nil {
				msg.cleanup()
			}
			m.stage = stageConfirm
			m.viewport.SetContent(m.buildConfirmContent())
			return m, nil
		}
		m.repackCleanup = msg.cleanup
		m.uploadPath = msg.report.UploadDirectory
		return m.startPlanning()
	case planDoneMsg:
		if msg.err != nil {
			m.cleanupRepackFiles()
			m.err = msg.err
			m.stage = stageDone
			m.resultMsg = fmt.Sprintf("Planning failed: %v", msg.err)
			return m, nil
		}
		m.plan = msg.plan
		m.approvalKnown = msg.approvalKnown
		m.approvalOpen = msg.approvalOpen
		m.approvalErr = msg.approvalErr
		m.stage = stageConfirm
		m.confirmCursor = m.initialConfirmCursor()
		m.viewport.SetContent(m.buildConfirmContent())
		return m, nil
	}
	return m, nil
}

func (m Model) viewScanning() string {
	view := fmt.Sprintf("\n  %s %s %s...\n", m.spinner.View(), m.scanPhase, m.effectiveUploadPath())
	if m.repackCancel != nil {
		phase := "Preparing temporary bundle"
		switch m.repackProgress.Phase {
		case repack.ProgressRepacking:
			phase = "Repacking"
		case repack.ProgressVerifying:
			phase = "Verifying SHA-256"
		case repack.ProgressComplete:
			phase = "Finalizing"
		}
		if m.repackProgress.FileIndex > 0 && m.repackProgress.FileCount > 0 {
			phase += fmt.Sprintf(" • book %d/%d", m.repackProgress.FileIndex, m.repackProgress.FileCount)
		}
		view += fmt.Sprintf("\n  %s\n", phase)
		if m.repackProgress.Path != "" {
			view += fmt.Sprintf("  %s\n", m.repackProgress.Path)
		}
		if m.repackProgress.Total > 0 {
			completed := min(max(int64(0), m.repackProgress.Bytes), m.repackProgress.Total)
			percent := float64(completed) / float64(m.repackProgress.Total) * 100
			view += fmt.Sprintf("  %.1f%% • %s / %s\n", percent, upload.FormatSize(completed), upload.FormatSize(m.repackProgress.Total))
		}
		workers := m.repackProgress.Workers
		if workers <= 0 {
			workers = repack.RecommendedWorkers()
		}
		elapsed := time.Duration(0)
		if !m.repackStartedAt.IsZero() {
			elapsed = time.Since(m.repackStartedAt)
		}
		view += fmt.Sprintf("  CPU workers: %d • elapsed %s\n", workers, formatElapsed(elapsed))
		view += "\n  Esc: cancel temporary repack\n"
	}
	return view
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
		if !m.repackOfferHandled && m.repackBookBytes() >= repack.DefaultOfferBytes {
			m.stage = stageRepackOffer
			return m, nil
		}
		return m.startCompliance()
	}
	return m.startRouteSelection()
}

func (m Model) updateRepackOffer(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "y", "enter":
			m.repackOfferHandled = true
			m.stage = stageScanning
			m.scanPhase = "Estimating Zstd book compression"
			dirPath := m.dirPath
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				report, err := repack.EstimateDirectory(ctx, dirPath, repack.Options{})
				return repackEstimateDoneMsg{report: report, err: err}
			})
		case "n", "q", "esc":
			m.repackOfferHandled = true
			return m.startCompliance()
		}
	}
	return m, nil
}

func (m Model) viewRepackOffer() string {
	return fmt.Sprintf(
		"\n  Your compressed Zstd books weigh %s.\n\n"+
			"  Analyze whether a verified level-10 repack could make them smaller?\n"+
			"  The analysis is read-only, but it can use significant CPU.\n\n"+
			"  y/Enter: analyze • n/Esc: skip\n",
		upload.FormatSize(m.repackBookBytes()),
	)
}

func (m Model) repackBookBytes() int64 {
	var total int64
	for _, file := range m.localFiles {
		if repack.IsBookPath(file.AbsPath) {
			total += file.Size
		}
	}
	return total
}

func (m Model) startCompliance() (Model, tea.Cmd) {
	m.stage = stageScanning
	m.scanPhase = "Checking math compliance"
	m.complianceOverride = false
	m.complianceConfirm = false
	dirPath := m.dirPath
	options := m.complianceOptions
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		result := upload.RunMathComplianceWithOptions(dirPath, options)
		return complianceDoneMsg{result: result}
	})
}

func (m Model) updateCompliance(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if m.complianceConfirm {
			switch key.String() {
			case "y", "enter":
				m.complianceConfirm = false
				m.complianceOverride = true
				return m.startRouteSelection()
			case "n", "q", "esc":
				m.complianceConfirm = false
				return m, nil
			}
			return m, nil
		}
		issues := complianceIssues(m.compliance)
		needsAcknowledgement := len(issues) > 0 || m.compliance.ValidateForPublish() != nil
		totalPages := complianceTotalPages(len(m.compliance.ModeSummaries))
		if len(issues) > 0 {
			totalPages = complianceTotalPages(len(issues))
		}
		switch key.String() {
		case "enter", "s":
			if needsAcknowledgement {
				m.complianceConfirm = true
				return m, nil
			}
			return m.startRouteSelection()
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
			m.resetTemporaryRepack()
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
	for _, tier := range m.compliance.TierResults {
		fmt.Fprintf(
			&b,
			"  %s: %s • max compliant bet level %.2fx\n",
			tier.Tier,
			strings.ToUpper(string(tier.Status)),
			tier.MaxCompliantBetLevel,
		)
	}
	if len(m.compliance.TierResults) > 0 {
		b.WriteString("  Bet level and currency are excluded from the verdict.\n")
		b.WriteString("\n")
	}

	issues := complianceIssues(m.compliance)
	needsAcknowledgement := len(issues) > 0 || m.compliance.ValidateForPublish() != nil
	page, totalPages := 0, 1
	if needsAcknowledgement {
		start, end, safePage, pages := compliancePageRange(len(issues), m.compliancePage)
		page, totalPages = safePage, pages
		b.WriteString("  Issues requiring acknowledgement:\n")
		if len(issues) == 0 {
			fmt.Fprintf(&b, "  • [WARN] %v\n", m.compliance.ValidateForPublish())
		} else {
			for _, check := range issues[start:end] {
				b.WriteString(complianceRenderIssue(check, max(32, m.width-8)))
			}
		}
	} else if len(m.compliance.ModeSummaries) > 0 {
		rows := m.compliance.ModeSummaries
		start, end, safePage, pages := compliancePageRange(len(rows), m.compliancePage)
		page, totalPages = safePage, pages
		pageRows := rows[start:end]
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
	if m.complianceConfirm {
		b.WriteString("  Compliance has issues. Continue with upload and publishing anyway? [y/N]\n")
		return b.String()
	}
	if needsAcknowledgement {
		if err := m.compliance.ValidateForPublish(); err != nil {
			fmt.Fprintf(&b, "  Compliance warning: %v\n", err)
		}
		b.WriteString("  Enter: acknowledge & continue • r: reload • q: go back\n")
	} else {
		b.WriteString("  Enter: continue • r: reload • q: go back\n")
	}

	return b.String()
}

func complianceIssues(result upload.ComplianceResult) []upload.ComplianceCheck {
	issues := make([]upload.ComplianceCheck, 0, result.FailuresCount+result.WarningsCount)
	for _, check := range result.Checks {
		switch check.Status {
		case upload.ComplianceFail, upload.ComplianceWarn, upload.ComplianceNotEvaluated:
			issues = append(issues, check)
		}
	}
	return issues
}

func complianceRenderIssue(check upload.ComplianceCheck, width int) string {
	status := strings.ToUpper(string(check.Status))
	context := ""
	if check.Mode != "" {
		context += " • mode " + check.Mode
	}
	if check.Tier != "" {
		context += " • tier " + check.Tier
	}
	message := fmt.Sprintf("[%s] %s%s: %s", status, check.Name, context, check.Details)
	lines := wrapRunes(message, max(20, width-4))
	var b strings.Builder
	for i, line := range lines {
		prefix := "      "
		if i == 0 {
			prefix = "  • "
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
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
	pages := totalRows / 5
	if totalRows%5 != 0 {
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
	start = safePage * 5
	end = start + 5
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

// --- Upload route / accelerator setup ---

func (m Model) startRouteSelection() (Model, tea.Cmd) {
	profiles, err := accelerator.LoadProfiles()
	if profiles == nil {
		profiles = &accelerator.Profiles{Profiles: make(map[string]accelerator.Profile)}
	}
	names := make([]string, 0, len(profiles.Profiles))
	for name := range profiles.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	m.stage = stageUploadRoute
	m.service = m.directService
	m.acceleratorProfiles = profiles
	m.acceleratorProfileNames = names
	m.acceleratorRouteCursor = 0
	preferred := strings.TrimSpace(os.Getenv("STAKE_ACCELERATOR"))
	for index, name := range names {
		profile := profiles.Profiles[name]
		if preferred == name || (preferred != "" && strings.EqualFold(strings.TrimSuffix(preferred, "/"), strings.TrimSuffix(profile.Address, "/"))) {
			m.acceleratorRouteCursor = index + 1
			break
		}
	}
	m.acceleratorErr = err
	m.acceleratorClient = nil
	m.acceleratorFingerprint = ""
	m.acceleratorSpeedResult = nil
	m.acceleratorSpeedErr = nil
	m.acceleratorPasswordInput.SetValue("")
	m.acceleratorPasswordInput.Blur()
	m.uploadRouteLabel = ""
	return m, nil
}

func (m Model) updateUploadRoute(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	last := len(m.acceleratorProfileNames) + 1
	switch key.String() {
	case "up", "k":
		if m.acceleratorRouteCursor > 0 {
			m.acceleratorRouteCursor--
		}
	case "down", "j":
		if m.acceleratorRouteCursor < last {
			m.acceleratorRouteCursor++
		}
	case "enter", " ", "space":
		switch m.acceleratorRouteCursor {
		case 0:
			m.service = m.directService
			m.uploadRouteLabel = "Direct to Stake"
			return m.startPlanning()
		case last:
			m.acceleratorAdding = true
			m.acceleratorSelectedName = ""
			m.acceleratorSelectedAddr = ""
			m.acceleratorAddressInput.SetValue("")
			m.acceleratorNameInput.SetValue("")
			m.acceleratorPasswordInput.SetValue("")
			m.acceleratorMakeDefault = m.acceleratorProfiles.Default == ""
			m.acceleratorFormFocus = 0
			m.acceleratorErr = nil
			m.stage = stageAcceleratorForm
			return m.focusAcceleratorForm()
		default:
			name := m.acceleratorProfileNames[m.acceleratorRouteCursor-1]
			profile := m.acceleratorProfiles.Profiles[name]
			m.acceleratorAdding = false
			m.acceleratorSelectedName = name
			m.acceleratorSelectedAddr = profile.Address
			password := ""
			preferred := strings.TrimSpace(os.Getenv("STAKE_ACCELERATOR"))
			if preferred == name || (preferred != "" && strings.EqualFold(strings.TrimSuffix(preferred, "/"), strings.TrimSuffix(profile.Address, "/"))) {
				password = os.Getenv("STAKE_ACCELERATOR_PASSWORD")
			}
			m.acceleratorPasswordInput.SetValue(password)
			m.acceleratorErr = nil
			m.stage = stageAcceleratorForm
			m.acceleratorPasswordInput.Focus()
			return m, textinput.Blink
		}
	case "q", "esc":
		if m.uploadType == "math" {
			m.stage = stageCompliance
			return m, nil
		}
		m.stage = stagePathInput
		m.pathFocus = 0
		m.pathInput.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m Model) viewUploadRoute() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Upload connection — %s/%s\n\n", m.team, m.game)
	b.WriteString("  Connection setup • Step 1 of 3\n\n")
	b.WriteString("  Choose how file bodies reach Stake:\n\n")
	b.WriteString(routeRow(m.acceleratorRouteCursor == 0, "Direct to Stake", "No relay"))
	for index, name := range m.acceleratorProfileNames {
		profile := m.acceleratorProfiles.Profiles[name]
		detail := profile.Address
		if profile.Fingerprint != "" {
			detail += " • identity pinned"
		}
		if name == m.acceleratorProfiles.Default {
			detail += " • default"
		}
		b.WriteString(routeRow(m.acceleratorRouteCursor == index+1, "Accelerator "+name, detail))
	}
	b.WriteString(routeRow(m.acceleratorRouteCursor == len(m.acceleratorProfileNames)+1, "Add a new accelerator", "Connect, authenticate and pin automatically"))
	if m.acceleratorErr != nil {
		fmt.Fprintf(&b, "\n  Warning: saved profiles could not be loaded: %v\n", m.acceleratorErr)
	}
	b.WriteString("\n  ↑/↓: select • Enter: continue • Esc: back\n")
	return b.String()
}

func routeRow(selected bool, title, detail string) string {
	prefix := "    "
	if selected {
		prefix = "  > "
	}
	return fmt.Sprintf("%s%s\n      %s\n", prefix, title, detail)
}

func (m Model) focusAcceleratorForm() (Model, tea.Cmd) {
	m.acceleratorAddressInput.Blur()
	m.acceleratorNameInput.Blur()
	m.acceleratorPasswordInput.Blur()
	switch m.acceleratorFormFocus {
	case 0:
		m.acceleratorAddressInput.Focus()
	case 1:
		m.acceleratorNameInput.Focus()
	case 2:
		m.acceleratorPasswordInput.Focus()
	default:
		return m, nil
	}
	return m, textinput.Blink
}

func (m Model) updateAcceleratorForm(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if !m.acceleratorAdding {
			switch key.String() {
			case "esc":
				m.acceleratorPasswordInput.SetValue("")
				m.stage = stageUploadRoute
				return m, nil
			case "enter":
				return m.beginAcceleratorConnect()
			}
			var cmd tea.Cmd
			m.acceleratorPasswordInput, cmd = m.acceleratorPasswordInput.Update(msg)
			return m, cmd
		}

		switch key.String() {
		case "esc":
			m.acceleratorPasswordInput.SetValue("")
			m.stage = stageUploadRoute
			return m, nil
		case "tab", "down", "j":
			if m.acceleratorFormFocus < 4 {
				m.acceleratorFormFocus++
			} else {
				m.acceleratorFormFocus = 0
			}
			return m.focusAcceleratorForm()
		case "shift+tab", "up", "k":
			if m.acceleratorFormFocus > 0 {
				m.acceleratorFormFocus--
			} else {
				m.acceleratorFormFocus = 4
			}
			return m.focusAcceleratorForm()
		case " ", "space":
			if m.acceleratorFormFocus == 3 {
				m.acceleratorMakeDefault = !m.acceleratorMakeDefault
				return m, nil
			}
		case "enter":
			switch m.acceleratorFormFocus {
			case 0, 1, 2:
				m.acceleratorFormFocus++
				return m.focusAcceleratorForm()
			case 3:
				m.acceleratorMakeDefault = !m.acceleratorMakeDefault
				m.acceleratorFormFocus = 4
				return m.focusAcceleratorForm()
			case 4:
				return m.beginAcceleratorConnect()
			}
		}
	}

	var cmd tea.Cmd
	switch m.acceleratorFormFocus {
	case 0:
		m.acceleratorAddressInput, cmd = m.acceleratorAddressInput.Update(msg)
	case 1:
		m.acceleratorNameInput, cmd = m.acceleratorNameInput.Update(msg)
	case 2:
		m.acceleratorPasswordInput, cmd = m.acceleratorPasswordInput.Update(msg)
	}
	return m, cmd
}

func (m Model) beginAcceleratorConnect() (Model, tea.Cmd) {
	password := strings.TrimSpace(m.acceleratorPasswordInput.Value())
	if password == "" {
		m.acceleratorErr = fmt.Errorf("accelerator password is required")
		if m.acceleratorAdding {
			m.acceleratorFormFocus = 2
			return m.focusAcceleratorForm()
		}
		return m, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	m.acceleratorCancel = cancel
	m.acceleratorErr = nil
	m.acceleratorSpeedResult = nil
	m.acceleratorSpeedErr = nil
	m.acceleratorBusyLabel = "Authenticating accelerator and verifying certificate pin"
	m.acceleratorBusyReturn = stageAcceleratorForm
	m.stage = stageAcceleratorBusy
	m.acceleratorPasswordInput.SetValue("")

	if !m.acceleratorAdding {
		reference := m.acceleratorSelectedName
		address := m.acceleratorSelectedAddr
		connect := m.connectExistingAccelerator
		return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
			client, name, err := connect(ctx, reference, password)
			return acceleratorConnectedMsg{client: client, profileName: name, address: address, fingerprint: acceleratorFingerprint(client), err: err}
		})
	}

	address := strings.TrimSpace(m.acceleratorAddressInput.Value())
	name := strings.TrimSpace(m.acceleratorNameInput.Value())
	if address == "" {
		cancel()
		m.acceleratorCancel = nil
		m.acceleratorErr = fmt.Errorf("accelerator IP or address is required")
		m.stage = stageAcceleratorForm
		m.acceleratorFormFocus = 0
		return m.focusAcceleratorForm()
	}
	if name == "" {
		name = accelerator.AutoProfileName(address)
		m.acceleratorNameInput.SetValue(name)
	}
	if _, exists := m.acceleratorProfiles.Profiles[name]; exists {
		cancel()
		m.acceleratorCancel = nil
		m.acceleratorErr = fmt.Errorf("profile %q already exists; choose it from the previous screen or use another name", name)
		m.stage = stageAcceleratorForm
		m.acceleratorFormFocus = 1
		return m.focusAcceleratorForm()
	}
	for existingName, profile := range m.acceleratorProfiles.Profiles {
		if strings.EqualFold(strings.TrimSuffix(profile.Address, "/"), strings.TrimSuffix(address, "/")) {
			cancel()
			m.acceleratorCancel = nil
			m.acceleratorErr = fmt.Errorf("this address is already saved as %q", existingName)
			m.stage = stageAcceleratorForm
			m.acceleratorFormFocus = 0
			return m.focusAcceleratorForm()
		}
	}
	makeDefault := m.acceleratorMakeDefault
	connect := m.connectNewAccelerator
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		client, profileName, err := connect(ctx, address, password, name, makeDefault)
		return acceleratorConnectedMsg{client: client, profileName: profileName, address: address, fingerprint: acceleratorFingerprint(client), newProfile: true, err: err}
	})
}

func acceleratorFingerprint(client *accelerator.Client) string {
	if client == nil {
		return ""
	}
	return client.Fingerprint()
}

func connectAndSaveAccelerator(ctx context.Context, address, password, name string, makeDefault bool) (*accelerator.Client, string, error) {
	client, err := accelerator.NewClient(address, password, "")
	if err != nil {
		return nil, "", err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, "", err
	}
	if err := accelerator.SaveProfile(name, accelerator.Profile{Address: address, Fingerprint: client.Fingerprint()}, makeDefault); err != nil {
		return nil, "", err
	}
	return client, name, nil
}

func (m Model) updateAcceleratorBusy(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "esc" || msg.String() == "q" {
			if m.acceleratorCancel != nil {
				m.acceleratorCancel()
			}
			m.acceleratorCancel = nil
			if m.acceleratorBusyReturn == stageAcceleratorReady {
				m.acceleratorSpeedErr = fmt.Errorf("speed test cancelled")
				m.stage = stageAcceleratorReady
				return m, nil
			}
			m.acceleratorErr = fmt.Errorf("connection cancelled")
			m.stage = stageAcceleratorForm
			if !m.acceleratorAdding {
				m.acceleratorPasswordInput.Focus()
				return m, textinput.Blink
			}
			return m.focusAcceleratorForm()
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case acceleratorConnectedMsg:
		if m.acceleratorCancel != nil {
			m.acceleratorCancel()
		}
		m.acceleratorCancel = nil
		if msg.err != nil {
			m.acceleratorErr = msg.err
			m.stage = stageAcceleratorForm
			if !m.acceleratorAdding {
				m.acceleratorPasswordInput.Focus()
				return m, textinput.Blink
			}
			m.acceleratorFormFocus = 2
			return m.focusAcceleratorForm()
		}
		m.acceleratorClient = msg.client
		m.acceleratorSelectedName = msg.profileName
		m.acceleratorSelectedAddr = msg.address
		m.acceleratorFingerprint = msg.fingerprint
		m.acceleratorNewlyPinned = msg.newProfile
		m.acceleratorReadyCursor = 0
		m.acceleratorErr = nil
		m.stage = stageAcceleratorReady
		return m, nil
	case acceleratorSpeedTestDoneMsg:
		if m.acceleratorCancel != nil {
			m.acceleratorCancel()
		}
		m.acceleratorCancel = nil
		m.acceleratorSpeedErr = msg.err
		if msg.err == nil {
			result := msg.result
			m.acceleratorSpeedResult = &result
		}
		m.stage = stageAcceleratorReady
		return m, nil
	}
	return m, nil
}

func (m Model) viewAcceleratorBusy() string {
	return fmt.Sprintf("\n  %s %s...\n\n  Esc: cancel\n", m.spinner.View(), m.acceleratorBusyLabel)
}

func (m Model) updateAcceleratorReady(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.acceleratorReadyCursor > 0 {
			m.acceleratorReadyCursor--
		}
	case "down", "j":
		if m.acceleratorReadyCursor < 2 {
			m.acceleratorReadyCursor++
		}
	case "s":
		return m.startAcceleratorSpeedTest()
	case "enter", " ", "space":
		switch m.acceleratorReadyCursor {
		case 0:
			return m.useAcceleratorAndPlan()
		case 1:
			return m.startAcceleratorSpeedTest()
		case 2:
			return m.startRouteSelection()
		}
	case "esc", "q":
		return m.startRouteSelection()
	}
	return m, nil
}

func (m Model) startAcceleratorSpeedTest() (Model, tea.Cmd) {
	if m.acceleratorClient == nil {
		m.acceleratorSpeedErr = fmt.Errorf("accelerator connection is unavailable")
		return m, nil
	}
	const size = int64(64 * 1024 * 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.acceleratorCancel = cancel
	m.acceleratorBusyLabel = "Running encrypted 64 MiB / 4-connection speed test"
	m.acceleratorBusyReturn = stageAcceleratorReady
	m.acceleratorSpeedErr = nil
	m.stage = stageAcceleratorBusy
	client := m.acceleratorClient
	run := m.runAcceleratorSpeedTest
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		result, err := run(ctx, client, size)
		return acceleratorSpeedTestDoneMsg{result: result, err: err}
	})
}

func (m Model) useAcceleratorAndPlan() (Model, tea.Cmd) {
	client := m.acceleratorClient
	if client == nil || m.directService == nil {
		m.acceleratorErr = fmt.Errorf("accelerator upload transport is unavailable")
		return m, nil
	}
	m.service = m.directService.WithUploadTransport(func(base upload.Client) upload.Client {
		return &accelerator.UploadClient{Client: base, Accelerator: client}
	})
	m.uploadRouteLabel = fmt.Sprintf("Accelerator %s — %s", m.acceleratorSelectedName, m.acceleratorSelectedAddr)
	return m.startPlanning()
}

func (m Model) viewAcceleratorForm() string {
	var b strings.Builder
	if !m.acceleratorAdding {
		fmt.Fprintf(&b, "\n  Connect accelerator %s\n\n", m.acceleratorSelectedName)
		b.WriteString("  Connection setup • Step 2 of 3\n\n")
		fmt.Fprintf(&b, "  Address: %s\n", m.acceleratorSelectedAddr)
		b.WriteString("  The saved certificate pin will be verified after password authentication.\n\n")
		b.WriteString("  Password\n  " + m.acceleratorPasswordInput.View() + "\n")
		if m.acceleratorErr != nil {
			fmt.Fprintf(&b, "\n  Error: %v\n", m.acceleratorErr)
		}
		b.WriteString("\n  Enter: connect • Esc: back\n")
		return b.String()
	}

	b.WriteString("\n  Add upload accelerator\n\n")
	b.WriteString("  Connection setup • Step 2 of 3\n\n")
	b.WriteString("  Only the password is needed. TLS identity is pinned automatically\n")
	b.WriteString("  after the server proves it knows that password.\n\n")
	b.WriteString(acceleratorFormInput(m.acceleratorFormFocus == 0, "IP or address", m.acceleratorAddressInput.View()))
	b.WriteString(acceleratorFormInput(m.acceleratorFormFocus == 1, "Profile name (optional)", m.acceleratorNameInput.View()))
	b.WriteString(acceleratorFormInput(m.acceleratorFormFocus == 2, "Password", m.acceleratorPasswordInput.View()))
	b.WriteString(acceleratorFormRow(m.acceleratorFormFocus == 3, checkbox(m.acceleratorMakeDefault)+" Make default accelerator"))
	b.WriteString(acceleratorFormRow(m.acceleratorFormFocus == 4, "[ Connect & pin ]"))
	if m.acceleratorErr != nil {
		fmt.Fprintf(&b, "\n  Error: %v\n", m.acceleratorErr)
	}
	b.WriteString("\n  ↑/↓ or Tab: move • Space: toggle • Enter: continue • Esc: back\n")
	return b.String()
}

func acceleratorFormInput(selected bool, label, value string) string {
	prefix := "    "
	if selected {
		prefix = "  > "
	}
	return fmt.Sprintf("%s%s\n    %s\n\n", prefix, label, value)
}

func acceleratorFormRow(selected bool, label string) string {
	prefix := "    "
	if selected {
		prefix = "  > "
	}
	return prefix + label + "\n"
}

func (m Model) viewAcceleratorReady() string {
	var b strings.Builder
	b.WriteString("\n  Accelerator connected\n\n")
	b.WriteString("  Connection setup • Step 3 of 3\n\n")
	fmt.Fprintf(&b, "  Profile: %s\n", m.acceleratorSelectedName)
	fmt.Fprintf(&b, "  Address: %s\n", m.acceleratorSelectedAddr)
	if m.acceleratorFingerprint != "" {
		fmt.Fprintf(&b, "  Certificate pin: sha256:%s\n", m.acceleratorFingerprint)
	}
	if m.acceleratorNewlyPinned {
		b.WriteString("  Identity authenticated, pinned and saved locally.\n")
	} else {
		b.WriteString("  Password and saved certificate pin verified.\n")
	}
	b.WriteString("  Stake SID is not sent to the accelerator.\n")
	if result := m.acceleratorSpeedResult; result != nil {
		clientRate := safeMiBRate(result.Bytes, result.ClientDuration)
		serverRate := safeMiBRate(result.Bytes, result.ServerDuration)
		fmt.Fprintf(&b, "\n  Speed test: %.1f MiB/s to relay • %.1f MiB/s receive/decrypt\n", clientRate, serverRate)
		fmt.Fprintf(&b, "  64 MiB completed in %s over %d connections\n", result.ClientDuration.Round(time.Millisecond), result.Connections)
	} else if m.acceleratorSpeedErr != nil {
		fmt.Fprintf(&b, "\n  Speed test failed: %v\n", m.acceleratorSpeedErr)
	}
	b.WriteString("\n")
	b.WriteString(acceleratorFormRow(m.acceleratorReadyCursor == 0, "[ Use for this upload ]"))
	b.WriteString(acceleratorFormRow(m.acceleratorReadyCursor == 1, "[ Run 64 MiB speed test ]"))
	b.WriteString(acceleratorFormRow(m.acceleratorReadyCursor == 2, "Back to connection choices"))
	b.WriteString("\n  ↑/↓: select • Enter: continue • s: speed test • Esc: back\n")
	return b.String()
}

func safeMiBRate(bytes int64, duration time.Duration) float64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return float64(bytes) / duration.Seconds() / (1024 * 1024)
}

func (m Model) startPlanning() (Model, tea.Cmd) {
	m.stage = stageScanning
	m.scanPhase = "Planning upload"
	team, game, uploadType, dirPath := m.team, m.game, m.uploadType, m.effectiveUploadPath()
	appService := m.service
	var compliance *upload.ComplianceOptions
	if uploadType == "math" {
		options := m.complianceOptions
		compliance = &options
	}
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		prepared, err := appService.PrepareUpload(context.Background(), service.UploadTarget{
			Team: team, Game: game, Type: uploadType, Path: dirPath, Compliance: compliance,
		})
		if err != nil {
			return planDoneMsg{err: err}
		}
		detail, approvalErr := appService.GetGame(context.Background(), team, game)
		message := planDoneMsg{plan: prepared.Plan, approvalErr: approvalErr}
		if approvalErr == nil && detail != nil && detail.Approval != nil {
			message.approvalKnown = true
			message.approvalOpen = detail.Approval.Open
		}
		return message
	})
}

func (m Model) updateConfirm(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y":
			return m.confirmUpload()
		case "enter", " ", "space":
			switch m.confirmCursor {
			case 0:
				m.togglePublishPreference()
				return m, nil
			case 1:
				m.toggleApprovalPreference()
				return m, nil
			case m.repackActionIndex():
				if m.repackOptionAvailable() {
					if m.temporaryRepackSelected() {
						m.resetTemporaryRepack()
						return m.startPlanning()
					}
					return m.startTemporaryRepack()
				}
				return m.confirmUpload()
			default:
				return m.confirmUpload()
			}
		case "n", "q", "esc":
			m.resetTemporaryRepack()
			m.stage = stagePathInput
			m.pathFocus = 0
			m.pathInput.Focus()
			return m, textinput.Blink
		case "r":
			return m.startRouteSelection()
		case "up", "k":
			if m.confirmCursor > 0 {
				m.confirmCursor--
			}
			return m, nil
		case "down", "j":
			if m.confirmCursor < m.confirmLastCursor() {
				m.confirmCursor++
			}
			return m, nil
		case "pgup", "pgdown":
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
		m.approvalKnown = msg.approvalKnown
		m.approvalOpen = msg.approvalOpen
		m.approvalErr = msg.approvalErr
		m.stage = stageConfirm
		m.confirmCursor = m.initialConfirmCursor()
		m.viewport.SetContent(m.buildConfirmContent())
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m Model) confirmUpload() (Model, tea.Cmd) {
	if m.publishAfterUpload && m.uploadType == "math" {
		if err := m.mathPublishComplianceError(); err != nil {
			m.stage = stageCompliance
			m.complianceConfirm = true
			return m, nil
		}
	}
	return m.startUpload()
}

func (m Model) initialConfirmCursor() int {
	if m.forcePublish {
		if m.approvalOpen {
			return 1
		}
		return m.confirmLastCursor()
	}
	return 0
}

func (m *Model) togglePublishPreference() {
	if m.forcePublish {
		return
	}
	m.publishAfterUpload = !m.publishAfterUpload
	m.preferences.PublishAfterUpload = m.publishAfterUpload
	if !m.publishAfterUpload {
		m.changeApproval = false
		m.preferences.ChangeApprovalVersion = false
	}
	m.savePreferences()
}

func (m *Model) toggleApprovalPreference() {
	if !m.approvalOpen {
		return
	}
	m.changeApproval = !m.changeApproval
	m.preferences.ChangeApprovalVersion = m.changeApproval
	if m.changeApproval {
		m.publishAfterUpload = true
		m.preferences.PublishAfterUpload = true
	}
	m.savePreferences()
}

func (m *Model) savePreferences() {
	path, err := saveUploadPreferences(m.preferences)
	if path != "" {
		m.preferencesPath = path
	}
	m.preferencesErr = err
}

func (m Model) approvalChangeSelected() bool {
	return m.changeApproval && m.approvalOpen
}

func checkbox(selected bool) string {
	if selected {
		return "[x]"
	}
	return "[ ]"
}

func confirmRow(selected bool, label string) string {
	prefix := "    "
	if selected {
		prefix = "  > "
	}
	return prefix + label + "\n"
}

func (m Model) approvalPreferenceLabel() string {
	if m.approvalErr != nil {
		return "[ ] Change approval version — unavailable (status check failed)"
	}
	if !m.approvalKnown {
		return "[ ] Change approval version — unavailable (no approval)"
	}
	if !m.approvalOpen {
		return "[ ] Change approval version — unavailable (approval closed)"
	}
	return checkbox(m.changeApproval) + " Change approval version"
}

func (m Model) publishPreferenceLabel() string {
	label := checkbox(m.publishAfterUpload) + " Publish"
	if m.forcePublish {
		label += " — required by publish command"
	}
	return label
}

func (m Model) uploadActionLabel() string {
	return "[ Upload ]"
}

func (m Model) repackOptionAvailable() bool {
	return m.uploadType == "math" && m.repackReport.RecommendedFiles > 0
}

func (m Model) repackActionIndex() int {
	if m.repackOptionAvailable() {
		return 2
	}
	return -1
}

func (m Model) uploadActionIndex() int {
	if m.repackOptionAvailable() {
		return 3
	}
	return 2
}

func (m Model) confirmLastCursor() int {
	return m.uploadActionIndex()
}

func (m Model) temporaryRepackSelected() bool {
	return m.repackCleanup != nil && m.repackApplyReport.TemporaryStaging && m.uploadPath != ""
}

func (m Model) repackActionLabel() string {
	if m.temporaryRepackSelected() {
		return "[ Use original books instead ]"
	}
	return fmt.Sprintf("[ Repack %d book(s) for this upload ]", m.repackReport.RecommendedFiles)
}

func (m Model) bundleBytes() int64 {
	var total int64
	for _, file := range m.localFiles {
		total += max(int64(0), file.Size)
	}
	return total
}

func (m Model) effectiveUploadPath() string {
	if m.uploadPath != "" {
		return m.uploadPath
	}
	return m.dirPath
}

func (m Model) startTemporaryRepack() (Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	progressCh := make(chan tea.Msg, 32)
	m.repackCancel = cancel
	m.repackApplyErr = nil
	m.repackApplyDone = false
	m.repackProgress = repack.Progress{Phase: repack.ProgressPreparing, Workers: repack.RecommendedWorkers()}
	m.repackProgressCh = progressCh
	m.repackStartedAt = time.Now()
	m.stage = stageScanning
	m.scanPhase = "Creating verified temporary Zstd level-10 repack"
	directory := m.dirPath
	estimate := m.repackReport
	go func() {
		defer close(progressCh)
		defer cancel()
		report, cleanup, err := repack.ApplyRecommendedWithProgress(
			ctx,
			directory,
			estimate,
			repack.Options{},
			func(progress repack.Progress) {
				select {
				case progressCh <- repackProgressMsg{progress: progress}:
				case <-ctx.Done():
				}
			},
		)
		result := repackApplyDoneMsg{report: report, cleanup: cleanup, err: err}
		select {
		case progressCh <- result:
		case <-ctx.Done():
			if cleanup != nil {
				cleanup()
			}
		}
	}()
	return m, tea.Batch(m.spinner.Tick, waitForRepackProgress(progressCh))
}

func waitForRepackProgress(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-ch
		if !ok {
			return nil
		}
		return message
	}
}

func (m *Model) cleanupRepackFiles() {
	if m.repackCancel != nil {
		m.repackCancel()
		m.repackCancel = nil
	}
	if m.repackCleanup != nil {
		m.repackCleanup()
		m.repackCleanup = nil
	}
	m.uploadPath = ""
	m.repackProgressCh = nil
	m.repackProgress = repack.Progress{}
	m.repackStartedAt = time.Time{}
}

func (m *Model) resetTemporaryRepack() {
	m.cleanupRepackFiles()
	m.repackApplyReport = repack.ApplyReport{}
	m.repackApplyErr = nil
	m.repackApplyDone = false
}

func (m Model) buildConfirmContent() string {
	var b strings.Builder
	plan := m.plan
	if m.uploadRouteLabel != "" {
		fmt.Fprintf(&b, "  Upload connection: %s\n\n", m.uploadRouteLabel)
	}

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
	if m.repackReport.RecommendedFiles > 0 {
		if m.temporaryRepackSelected() {
			fmt.Fprintf(&b, "\n  Temporary repack ready: %d book(s).\n", m.repackApplyReport.AppliedFiles)
			fmt.Fprintf(&b, "  Actual saving: %s (%.1f%%).\n",
				upload.FormatSize(max(int64(0), m.repackApplyReport.SavingsBytes)),
				m.repackApplyReport.SavingsPercent(),
			)
			fmt.Fprintf(&b, "  Upload source: %s\n", m.repackApplyReport.UploadDirectory)
			b.WriteString("  Original books are unchanged.\n")
			b.WriteString("  Temporary files are deleted after upload, cancel, or exit.\n")
		} else {
			resources := repack.EstimateResources(m.repackReport, m.bundleBytes())
			fmt.Fprintf(&b, "\n  Compression opportunity: %d book(s).\n", m.repackReport.RecommendedFiles)
			fmt.Fprintf(&b, "  Estimated saving: %s (%.1f%%).\n",
				upload.FormatSize(max(int64(0), m.repackReport.RecommendedSavingsBytes)),
				m.repackReport.RecommendedSavingsPercent(),
			)
			b.WriteString("  Level 10 streams decompressed JSONL directly into Zstd.\n")
			b.WriteString("  Plaintext JSON is never written to temporary storage.\n")
			fmt.Fprintf(&b, "  Compression workers: %d CPU threads (automatic, capped).\n", resources.Workers)
			fmt.Fprintf(&b, "  Temporary workspace: %s\n", resources.TemporaryDirectory)
			fmt.Fprintf(&b, "  Temporary space: up to ~%s. Working RAM: ~%s.\n",
				upload.FormatSize(resources.PeakTemporaryBytes), upload.FormatSize(resources.PeakMemoryBytes))
			b.WriteString("  A RAM-backed temp directory also consumes RAM for temporary space.\n")
			b.WriteString("  Originals stay read-only; temporary files are removed automatically.\n")
		}
		if m.repackApplyDone && !m.temporaryRepackSelected() && m.repackApplyErr == nil {
			b.WriteString("  Actual savings did not meet the threshold; originals remain selected.\n")
		}
		if m.repackApplyErr != nil {
			fmt.Fprintf(&b, "  Temporary repack failed; originals remain selected: %v\n", m.repackApplyErr)
		}
	} else if m.repackEstimateErr != nil {
		fmt.Fprintf(&b, "\n  Compression estimate unavailable: %v\n", m.repackEstimateErr)
	}

	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Upload %s to %s / %s\n\n", m.uploadType, m.team, m.game)
	b.WriteString(m.viewport.View())
	b.WriteString("\n\n  Upload options:\n")
	publishErr := error(nil)
	if m.uploadType == "math" {
		publishErr = m.mathPublishComplianceError()
	}
	if publishErr != nil {
		fmt.Fprintf(&b, "  Compliance warning: %v\n\n", publishErr)
	}
	b.WriteString(confirmRow(m.confirmCursor == 0, m.publishPreferenceLabel()))
	b.WriteString(confirmRow(m.confirmCursor == 1, m.approvalPreferenceLabel()))
	if m.repackOptionAvailable() {
		b.WriteString(confirmRow(m.confirmCursor == m.repackActionIndex(), m.repackActionLabel()))
	}
	b.WriteString("\n")
	b.WriteString(confirmRow(m.confirmCursor == m.uploadActionIndex(), m.uploadActionLabel()))
	if m.preferencesErr != nil {
		fmt.Fprintf(&b, "\n  Warning: preferences were not saved: %v\n", m.preferencesErr)
	} else if m.preferencesPath != "" {
		b.WriteString("\n  Settings are saved automatically.\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\n  %v\n", m.err)
	}
	b.WriteString("\n  ↑/↓: select • Space/Enter: toggle or upload • y: upload • r: connection • Esc: cancel • PgUp/PgDn: scroll\n")
	return b.String()
}

func (m Model) mathPublishComplianceError() error {
	if m.uploadType != "math" || m.complianceOverride {
		return nil
	}
	return m.compliance.ValidateForPublish()
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
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return bytesTickMsg{}
	})
}

func sampleThroughput(samples []throughputSample, now time.Time, bytes int64) ([]throughputSample, float64) {
	const window = 4 * time.Second
	if len(samples) > 0 && bytes < samples[len(samples)-1].bytes {
		samples = nil
	}
	samples = append(samples, throughputSample{at: now, bytes: bytes})
	cutoff := now.Add(-window)
	first := 0
	for first+1 < len(samples) && samples[first+1].at.Before(cutoff) {
		first++
	}
	samples = samples[first:]
	if len(samples) < 2 {
		return samples, 0
	}
	elapsed := samples[len(samples)-1].at.Sub(samples[0].at).Seconds()
	if elapsed < 0.5 {
		return samples, 0
	}
	delta := samples[len(samples)-1].bytes - samples[0].bytes
	if delta <= 0 {
		return samples, 0
	}
	return samples, float64(delta) / elapsed
}

func (m Model) startUpload() (Model, tea.Cmd) {
	m.stage = stageUploading
	m.progressTotal = m.plan.TotalActions()
	m.progressCurrent = 0
	m.progressFile = ""
	m.progressErrors = nil
	m.startTime = time.Now()
	m.bytesTotal = m.plan.TotalUploadBytes()
	m.transferTracker = upload.NewTransferTracker(m.plan.ToUpload)
	m.transferState = m.transferTracker.Snapshot()
	m.rateSamples = nil
	m.fileRateSamples = make(map[string][]throughputSample)
	m.fileRates = make(map[string]float64)
	m.overallRate = 0
	m.uploadRunErr = nil
	m.uploadScroll = 0
	m.errorLogPath = ""
	m.err = nil
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
	appService := m.service
	tracker := m.transferTracker

	go func() {
		defer close(ch)
		err := appService.ExecutePlan(ctx, plan, service.ExecuteOptions{
			Gate:    gate,
			Tracker: tracker,
			Source:  "tui",
			Path:    m.dirPath,
			Progress: func(evt upload.ProgressEvent) {
				select {
				case ch <- evt:
				case <-ctx.Done():
				}
			},
		})
		ch <- upload.ProgressEvent{Phase: "finished", Error: err}
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
		case "up", "k":
			if m.uploadScroll > 0 {
				m.uploadScroll--
			}
			return m, nil
		case "down", "j":
			if m.uploadScroll+1 < len(m.transferState.Files) {
				m.uploadScroll++
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
		if m.transferTracker != nil {
			now := time.Now()
			m.transferState = m.transferTracker.Snapshot()
			sent := m.transferState.UploadedBytes
			m.rateSamples, m.overallRate = sampleThroughput(m.rateSamples, now, sent)
			for _, file := range m.transferState.Files {
				samples, rate := sampleThroughput(m.fileRateSamples[file.Name], now, file.Uploaded)
				m.fileRateSamples[file.Name] = samples
				m.fileRates[file.Name] = rate
			}
			percent := 0.0
			if m.bytesTotal > 0 {
				percent = float64(sent) / float64(m.bytesTotal)
				if percent > 1.0 {
					percent = 1.0
				}
				if percent >= 1.0 && !allTransfersComplete(m.transferState) {
					percent = 0.9999
				}
			}
			cmd := m.progress.SetPercent(percent)
			return m, tea.Batch(cmd, bytesTickCmd())
		}
		return m, bytesTickCmd()
	case uploadProgressMsg:
		if msg.Phase == "finished" {
			m.uploadRunErr = msg.Error
			return m, waitForUploadProgress(m.uploadCh)
		}
		if msg.Phase == "start" {
			return m, waitForUploadProgress(m.uploadCh)
		}
		m.progressCurrent = msg.Current
		m.progressFile = msg.FileName
		if msg.Error != nil {
			m.progressErrors = append(m.progressErrors, fmt.Sprintf("%s: %v", msg.FileName, msg.Error))
		}
		return m, waitForUploadProgress(m.uploadCh)
	case uploadFinishedMsg:
		m.cleanupRepackFiles()
		m.stage = stageDone
		m.doneCursor = 0
		m.publishInFlight = false
		m.publishMsg = ""
		m.publishErr = nil
		elapsed := time.Since(m.startTime)
		if m.uploadRunErr != nil && len(m.progressErrors) == 0 {
			m.progressErrors = append(m.progressErrors, m.uploadRunErr.Error())
		}
		if len(m.progressErrors) > 0 {
			m.err = fmt.Errorf("%d errors during upload", len(m.progressErrors))
			m.resultMsg = fmt.Sprintf("Upload completed with %d errors in %s", len(m.progressErrors), elapsed.Round(time.Second))
			m.uploadSucceeded = false
			m.errorLogPath = writeUploadErrorLog(m.progressErrors)
			m.viewport.SetContent(buildErrorContent(m.progressErrors, min(100, max(30, m.width-8))))
			m.viewport.GotoTop()
		} else if m.progressCurrent < m.progressTotal {
			m.resultMsg = "Upload cancelled"
			m.uploadSucceeded = false
		} else {
			m.resultMsg = fmt.Sprintf("Upload complete! %d files synced in %s",
				m.plan.TotalActions(), elapsed.Round(time.Second))
			m.uploadSucceeded = true
			if m.publishAfterUpload {
				m.publishInFlight = true
				return m, tea.Batch(m.spinner.Tick, m.doPublish())
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) viewUploading() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Uploading to %s/%s (%s)\n\n", m.team, m.game, m.uploadType)
	if !m.startTime.IsZero() {
		fmt.Fprintf(&b, "  Elapsed: %s\n\n", formatElapsed(time.Since(m.startTime)))
	}

	b.WriteString("  ")
	b.WriteString(m.progress.View())
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %d / %d actions\n", m.progressCurrent, m.progressTotal)
	bytesUploaded := m.transferState.UploadedBytes
	confirmedBytes := m.transferState.ConfirmedBytes
	inFlightBytes := max(int64(0), bytesUploaded-confirmedBytes)
	idleFor := activityIdleFor(m.transferState.LastActivity)
	fmt.Fprintf(&b, "  Sent to network: %s / %s", upload.FormatSize(bytesUploaded), upload.FormatSize(m.bytesTotal))
	if m.overallRate > 0 && idleFor < time.Second {
		fmt.Fprintf(&b, " • %s/s", upload.FormatSize(int64(m.overallRate)))
		if remaining := m.bytesTotal - bytesUploaded; remaining > 0 {
			fmt.Fprintf(&b, " • ETA %s", formatETA(remaining, m.overallRate))
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Confirmed by S3: %s • In flight: %s", upload.FormatSize(confirmedBytes), upload.FormatSize(inFlightBytes))
	sendingParts, confirmingParts, _ := transferPartCounts(m.transferState)
	if sendingParts > 0 || confirmingParts > 0 {
		fmt.Fprintf(&b, " • PUT: %d sending / %d confirming", sendingParts, confirmingParts)
	}
	if status := transferActivityStatus(m.transferState, idleFor); status != "" {
		fmt.Fprintf(&b, " • %s", status)
	}
	b.WriteString("\n\n")

	files := append([]upload.FileProgress(nil), m.transferState.Files...)
	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := uploadStatusRank(files[i].Status), uploadStatusRank(files[j].Status)
		if ri != rj {
			return ri < rj
		}
		return files[i].Name < files[j].Name
	})
	maxFiles := (m.height - 18) / 2
	if maxFiles < 2 {
		maxFiles = 2
	}
	start := m.uploadScroll
	if start > len(files)-1 {
		start = max(0, len(files)-1)
	}
	end := min(len(files), start+maxFiles)
	if len(files) > 0 {
		fmt.Fprintf(&b, "  Files %d–%d of %d:\n", start+1, end, len(files))
	}
	for _, file := range files[start:end] {
		percent := 0.0
		if file.Size > 0 {
			percent = 100 * float64(file.Uploaded) / float64(file.Size)
			if percent >= 100 && file.Status != "complete" {
				percent = 99.99
			}
		}
		fmt.Fprintf(&b, "  %s %6.2f%%  %s\n", uploadStatusIcon(file.Status, m.spinner.View()), percent, file.Name)
		fileInFlight := max(int64(0), file.Uploaded-file.Confirmed)
		fileIdleFor := activityIdleFor(file.LastActivity)
		fmt.Fprintf(&b, "      sent %s / %s • confirmed %s • in flight %s",
			upload.FormatSize(file.Uploaded), upload.FormatSize(file.Size),
			upload.FormatSize(file.Confirmed), upload.FormatSize(fileInFlight))
		if rate := m.fileRates[file.Name]; rate > 0 && file.Status != "complete" && fileIdleFor < time.Second {
			fmt.Fprintf(&b, " • %s/s", upload.FormatSize(int64(rate)))
			if remaining := file.Size - file.Uploaded; remaining > 0 {
				fmt.Fprintf(&b, " • ETA %s", formatETA(remaining, rate))
			}
		}
		fmt.Fprintf(&b, " • parts %d/%d", file.CompletedParts, file.TotalParts)
		if file.ActiveParts > 0 {
			fmt.Fprintf(&b, " (+%d active)", file.ActiveParts)
		}
		if file.ConfirmingParts > 0 {
			fmt.Fprintf(&b, " (+%d confirming)", file.ConfirmingParts)
		}
		if file.Status == "finalizing" {
			b.WriteString(" • finalizing with S3")
		}
		if status := fileActivityStatus(file, fileIdleFor); status != "" {
			fmt.Fprintf(&b, " • %s", status)
		}
		if file.Retries > 0 {
			fmt.Fprintf(&b, " • retries %d", file.Retries)
		}
		b.WriteString("\n")
		if file.LastError != "" && (file.Status == "retrying" || file.Status == "failed") {
			fmt.Fprintf(&b, "      Last error: %s\n", file.LastError)
		}
	}

	if m.uploadGate != nil && m.uploadGate.IsPaused() {
		b.WriteString("\n  ⏸  PAUSED\n")
	}

	if len(m.progressErrors) > 0 {
		fmt.Fprintf(&b, "\n  Errors (%d):\n", len(m.progressErrors))
		for _, e := range m.progressErrors {
			fmt.Fprintf(&b, "    ✗ %s\n", e)
		}
	}

	b.WriteString("\n  j/k: scroll files • p: pause/resume • c: cancel\n")
	return b.String()
}

func uploadStatusRank(status string) int {
	switch status {
	case "failed":
		return 0
	case "retrying":
		return 1
	case "uploading", "starting", "finalizing":
		return 2
	case "pending":
		return 3
	case "complete":
		return 4
	default:
		return 5
	}
}

func uploadStatusIcon(status, spinnerView string) string {
	switch status {
	case "complete":
		return "✓"
	case "failed":
		return "✗"
	case "retrying":
		return "↻"
	case "uploading", "starting", "finalizing":
		return spinnerView
	default:
		return "·"
	}
}

func formatETA(remaining int64, rate float64) string {
	if remaining <= 0 || rate <= 0 {
		return "—"
	}
	eta := time.Duration(float64(remaining)/rate) * time.Second
	if eta < time.Second {
		return "<1s"
	}
	return eta.Round(time.Second).String()
}

func allTransfersComplete(snapshot upload.TransferSnapshot) bool {
	if len(snapshot.Files) == 0 {
		return false
	}
	for _, file := range snapshot.Files {
		if file.Status != "complete" {
			return false
		}
	}
	return true
}

func activityIdleFor(lastActivity time.Time) time.Duration {
	if lastActivity.IsZero() {
		return 0
	}
	idle := time.Since(lastActivity)
	if idle < 0 {
		return 0
	}
	return idle
}

func transferActivityStatus(snapshot upload.TransferSnapshot, idle time.Duration) string {
	if idle < time.Second {
		return ""
	}
	active, confirming, finalizing := transferPartCounts(snapshot)
	wait := formatActivityDuration(idle)
	switch {
	case active > 0:
		return fmt.Sprintf("no network progress for %s (%d PUT active)", wait, active)
	case confirming > 0:
		return fmt.Sprintf("waiting for S3 for %s (%d PUT confirming)", wait, confirming)
	case finalizing > 0:
		return fmt.Sprintf("S3 finalizing for %s", wait)
	default:
		return ""
	}
}

func transferPartCounts(snapshot upload.TransferSnapshot) (active, confirming, finalizing int) {
	for _, file := range snapshot.Files {
		active += file.ActiveParts
		confirming += file.ConfirmingParts
		if file.Status == "finalizing" {
			finalizing++
		}
	}
	return active, confirming, finalizing
}

func fileActivityStatus(file upload.FileProgress, idle time.Duration) string {
	if idle < time.Second {
		return ""
	}
	wait := formatActivityDuration(idle)
	switch {
	case file.ActiveParts > 0:
		return "no network progress for " + wait
	case file.ConfirmingParts > 0:
		return "waiting for S3 for " + wait
	default:
		return ""
	}
}

func formatActivityDuration(value time.Duration) string {
	return value.Round(100 * time.Millisecond).String()
}

func formatElapsed(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	totalSeconds := int64(value / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
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
			if m.publishAfterUpload {
				m.err = msg.err
				m.resultMsg = "Upload complete, automatic publish failed"
			}
			return m, nil
		}
		if msg.result != nil && msg.result.IsError() {
			m.publishErr = fmt.Errorf("publish %s error [%s]: %s", m.uploadType, msg.result.Code, msg.result.Error())
			m.publishMsg = ""
			if m.publishAfterUpload {
				m.err = m.publishErr
				m.resultMsg = "Upload complete, automatic publish failed"
			}
			return m, nil
		}
		if msg.result != nil {
			m.publishErr = nil
			m.published = true
			m.doneCursor = 0
			m.publishMsg = fmt.Sprintf("Published %s v%d (changed: %v)", m.uploadType, msg.result.Version, msg.result.Changed)
			switch {
			case msg.approvalErr != nil:
				m.publishMsg += fmt.Sprintf("\nWarning: approval version was not updated: %v", msg.approvalErr)
			case msg.approvalUpdate != nil && msg.approvalUpdate.Warning != "":
				m.publishMsg += fmt.Sprintf("\nWarning: %s (%s remains v%d; published v%d)",
					msg.approvalUpdate.Warning,
					msg.approvalUpdate.Type,
					msg.approvalUpdate.PreviousVersion,
					msg.approvalUpdate.TargetVersion,
				)
			case msg.approvalUpdate != nil && msg.approvalUpdate.Updated:
				m.publishMsg += fmt.Sprintf("\nApproval %s version updated: v%d -> v%d",
					msg.approvalUpdate.Type,
					msg.approvalUpdate.PreviousVersion,
					msg.approvalUpdate.TargetVersion,
				)
			case msg.approvalUpdate != nil:
				m.publishMsg += fmt.Sprintf("\nApproval already uses %s v%d", msg.approvalUpdate.Type, msg.approvalUpdate.TargetVersion)
			}
			if m.publishAfterUpload {
				m.err = nil
				m.resultMsg = fmt.Sprintf("Upload & publish complete in %s", time.Since(m.startTime).Round(time.Second))
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.publishInFlight {
			return m, nil
		}
		if !m.uploadSucceeded && len(m.progressErrors) > 0 {
			switch msg.String() {
			case "up", "k", "down", "j", "pgup", "pgdown":
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		}
		canPublish := m.uploadSucceeded && !m.published
		switch msg.String() {
		case "q", "esc":
			return m, cleanupAndGoBack(m.errorLogPath)
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
				return m, cleanupAndGoBack(m.errorLogPath)
			}
			m.publishInFlight = true
			m.publishErr = nil
			m.publishMsg = ""
			return m, tea.Batch(m.spinner.Tick, m.doPublish())
		}
	}
	return m, nil
}

func cleanupAndGoBack(path string) tea.Cmd {
	return func() tea.Msg {
		if path != "" {
			_ = os.Remove(path)
		}
		return GoBackMsg{}
	}
}

// Cleanup removes transient diagnostic files created by this model.
func (m Model) Cleanup() {
	if m.acceleratorCancel != nil {
		m.acceleratorCancel()
	}
	if m.errorLogPath != "" {
		_ = os.Remove(m.errorLogPath)
	}
	m.cleanupRepackFiles()
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
		if m.errorLogPath != "" {
			fmt.Fprintf(&b, "  Full error log: %s\n\n", m.errorLogPath)
		}
		b.WriteString(m.viewport.View())
		b.WriteString("\n  j/k or PgUp/PgDn: scroll errors\n")
	}

	if m.publishInFlight {
		fmt.Fprintf(&b, "\n  %s Publishing %s...\n", m.spinner.View(), m.uploadType)
		if !m.startTime.IsZero() {
			fmt.Fprintf(&b, "  Total elapsed: %s\n", formatElapsed(time.Since(m.startTime)))
		}
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

func buildErrorContent(messages []string, width int) string {
	if width < 10 {
		width = 10
	}
	var b strings.Builder
	for _, message := range messages {
		lines := strings.Split(message, "\n")
		for lineIndex, line := range lines {
			wrapped := wrapRunes(line, width-4)
			for wrappedIndex, part := range wrapped {
				prefix := "      "
				if lineIndex == 0 && wrappedIndex == 0 {
					prefix = "    ✗ "
				}
				b.WriteString(prefix)
				b.WriteString(part)
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func writeUploadErrorLog(messages []string) string {
	file, err := os.CreateTemp("", "stakecli-upload-errors-*.log")
	if err != nil {
		return ""
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()

	if _, err := fmt.Fprintf(file, "Stake CLI upload errors\nGenerated: %s\n\n", time.Now().Format(time.RFC3339)); err != nil {
		return ""
	}
	for i, message := range messages {
		if _, err := fmt.Fprintf(file, "[%d] %s\n\n", i+1, message); err != nil {
			return ""
		}
	}
	if err := file.Sync(); err != nil {
		return ""
	}
	remove = false
	return path
}

func wrapRunes(value string, width int) []string {
	if width < 1 {
		width = 1
	}
	runes := []rune(value)
	if len(runes) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > 0 {
		end := min(width, len(runes))
		lines = append(lines, string(runes[:end]))
		runes = runes[end:]
	}
	return lines
}

func (m Model) doPublish() tea.Cmd {
	team, game, uploadType, path := m.team, m.game, m.uploadType, m.dirPath
	compliance := m.complianceOptions
	allowComplianceFailures := m.complianceOverride
	updateApproval := m.forceApproval || m.approvalChangeSelected()
	appService := m.service
	return func() tea.Msg {
		var result *stake.PublishResult
		var err error
		if uploadType == "math" {
			result, err = appService.PublishMath(context.Background(), service.UploadTarget{
				Team: team, Game: game, Type: uploadType, Path: path, Compliance: &compliance,
			}, allowComplianceFailures)
		} else {
			result, err = appService.Publish(context.Background(), team, game, uploadType)
		}
		message := publishDoneMsg{result: result, err: err}
		if err == nil && result != nil && !result.IsError() && updateApproval {
			message.approvalUpdate, message.approvalErr = appService.UpdateOpenApprovalVersion(
				context.Background(), team, game, uploadType, result.Version,
			)
		}
		return message
	}
}

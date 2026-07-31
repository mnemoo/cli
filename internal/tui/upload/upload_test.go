package upload

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/repack"
	"github.com/mnemoo/cli/internal/stake"
	coreupload "github.com/mnemoo/cli/internal/upload"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stakecli-upload-test-config-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("STAKE_CONFIG_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestSampleThroughputUsesRecentWindow(t *testing.T) {
	start := time.Unix(0, 0)
	var samples []throughputSample
	samples, _ = sampleThroughput(samples, start, 0)
	samples, rate := sampleThroughput(samples, start.Add(time.Second), 1024)
	if rate != 1024 {
		t.Fatalf("rate = %f, want 1024", rate)
	}

	samples, rate = sampleThroughput(samples, start.Add(2*time.Second), 100)
	if rate != 0 || len(samples) != 1 {
		t.Fatalf("counter reset should reset samples: rate=%f samples=%d", rate, len(samples))
	}
}

func TestFormatETA(t *testing.T) {
	if got := formatETA(10*1024, 1024); got != "10s" {
		t.Fatalf("ETA = %q, want 10s", got)
	}
}

func TestFormatElapsed(t *testing.T) {
	if got := formatElapsed(time.Hour + 2*time.Minute + 3*time.Second); got != "01:02:03" {
		t.Fatalf("elapsed = %q, want 01:02:03", got)
	}
}

func TestUploadingViewKeepsFullError(t *testing.T) {
	message := strings.Repeat("full S3 error details ", 80)
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageUploading
	model.transferState = coreupload.TransferSnapshot{Files: []coreupload.FileProgress{{
		Name: "/math/book.zst", Size: 100, Status: "failed", LastError: message, TotalParts: 1,
	}}}
	model.fileRates = map[string]float64{}
	view := model.viewUploading()
	if !strings.Contains(view, message) {
		t.Fatal("upload error details were truncated")
	}
}

func TestWriteUploadErrorLogKeepsCompleteMessages(t *testing.T) {
	message := strings.Repeat("complete error details ", 200)
	path := writeUploadErrorLog([]string{message})
	if path == "" {
		t.Fatal("error log was not created")
	}
	defer func() { _ = os.Remove(path) }()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading error log: %v", err)
	}
	if !strings.Contains(string(data), message) {
		t.Fatal("error log truncated the message")
	}
}

func TestUploadingViewDoesNotShowHundredPercentBeforeConfirmation(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageUploading
	model.transferState = coreupload.TransferSnapshot{Files: []coreupload.FileProgress{{
		Name: "/math/book.zst", Size: 100, Uploaded: 100, Status: "uploading",
		ConfirmingParts: 1, TotalParts: 1,
	}}}
	model.fileRates = map[string]float64{}
	view := model.viewUploading()
	if strings.Contains(view, "100.00%") {
		t.Fatal("unconfirmed upload was displayed as 100%")
	}
	if !strings.Contains(view, "99.99%") || !strings.Contains(view, "(+1 confirming)") {
		t.Fatalf("confirmation state missing from view: %s", view)
	}
}

func TestUploadingViewShowsReactiveNetworkState(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageUploading
	lastActivity := time.Now().Add(-3 * time.Second)
	model.bytesTotal = 100
	model.transferState = coreupload.TransferSnapshot{
		UploadedBytes: 50, ConfirmedBytes: 10, TotalBytes: 100, LastActivity: lastActivity,
		Files: []coreupload.FileProgress{{
			Name: "/math/book.zst", Size: 100, Uploaded: 50, Confirmed: 10,
			Status: "uploading", ActiveParts: 1, TotalParts: 1, LastActivity: lastActivity,
		}},
	}
	model.fileRates = map[string]float64{}
	view := model.viewUploading()
	for _, expected := range []string{
		"Sent to network: 50 B / 100 B",
		"Confirmed by S3: 10 B • In flight: 40 B",
		"no network progress for",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view does not contain %q: %s", expected, view)
		}
	}
}

func TestConfirmViewUsesUploadActionWithPersistentOptions(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.confirmCursor = 0
	view := model.viewConfirm()
	for _, expected := range []string{
		"> [x] Publish",
		"[ ] Change approval version — unavailable (no approval)",
		"[ Upload ]",
		"Settings are saved automatically.",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("confirm option %q missing: %s", expected, view)
		}
	}
	if strings.Contains(view, "Upload & Publish") || strings.Contains(view, "Upload Only") {
		t.Fatalf("legacy mutually exclusive actions are still visible: %s", view)
	}
}

func TestConfirmViewOffersVerifiedTemporaryRepack(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.uploadType = "math"
	model.plan = &coreupload.UploadPlan{}
	model.repackReport = repack.Report{
		OriginalBytes:             1024 * 1024 * 1024,
		EstimatedBytes:            800 * 1024 * 1024,
		EstimatedSavingsBytes:     224 * 1024 * 1024,
		RecommendedFiles:          2,
		RecommendedOriginalBytes:  1024 * 1024 * 1024,
		RecommendedEstimatedBytes: 800 * 1024 * 1024,
		RecommendedSavingsBytes:   224 * 1024 * 1024,
	}
	model.viewport.SetContent(model.buildConfirmContent())
	view := model.viewConfirm()
	for _, expected := range []string{
		"Compression opportunity: 2 book(s)",
		"Estimated saving: 224.0 MiB",
		"Compression workers:",
		"Working RAM:",
		"Plaintext JSON is never written",
		"Originals stay read-only",
		"[ Repack 2 book(s) for this upload ]",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("repack estimate %q missing: %s", expected, view)
		}
	}
}

func TestScanningViewShowsTemporaryRepackProgress(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageScanning
	model.dirPath = "/bundle"
	model.scanPhase = "Creating verified temporary Zstd level-10 repack"
	model.repackCancel = func() {}
	model.repackStartedAt = time.Now().Add(-3 * time.Second)
	model.repackProgress = repack.Progress{
		Phase: repack.ProgressVerifying, Path: "books_bonus.jsonl.zst",
		FileIndex: 2, FileCount: 3, Bytes: 50, Total: 100, Workers: 4,
	}
	view := model.viewScanning()
	for _, expected := range []string{
		"Verifying SHA-256 • book 2/3",
		"books_bonus.jsonl.zst",
		"50.0%",
		"CPU workers: 4",
		"elapsed",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("repack progress %q missing: %s", expected, view)
		}
	}
}

func TestConfirmRepackActionStartsTemporaryApply(t *testing.T) {
	directory := t.TempDir()
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.uploadType = "math"
	model.dirPath = directory
	model.plan = &coreupload.UploadPlan{}
	model.repackReport = repack.Report{
		Directory: directory, RecommendedFiles: 1,
		RecommendedOriginalBytes:  200 * 1024 * 1024,
		RecommendedEstimatedBytes: 150 * 1024 * 1024,
		RecommendedSavingsBytes:   50 * 1024 * 1024,
	}
	model.confirmCursor = model.repackActionIndex()

	updated, cmd := model.updateConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || updated.stage != stageScanning || updated.repackCancel == nil ||
		!strings.Contains(updated.scanPhase, "temporary Zstd level-10 repack") {
		t.Fatalf("repack action did not start: stage=%d cancel=%v phase=%q cmd=%v",
			updated.stage, updated.repackCancel != nil, updated.scanPhase, cmd != nil)
	}
	updated.resetTemporaryRepack()
}

func TestTemporaryRepackIsPlannedThenCleanedAfterUpload(t *testing.T) {
	staging := t.TempDir()
	cleaned := false
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageScanning
	model.uploadType = "math"
	model.dirPath = t.TempDir()
	model.repackReport = repack.Report{RecommendedFiles: 1}

	updated, cmd := model.updateScanning(repackApplyDoneMsg{
		report: repack.ApplyReport{
			UploadDirectory: staging, TemporaryStaging: true, AppliedFiles: 1,
			OriginalBytes: 200, RepackedBytes: 150, SavingsBytes: 50,
		},
		cleanup: func() {
			cleaned = true
			_ = os.RemoveAll(staging)
		},
	})
	if cmd == nil || updated.effectiveUploadPath() != staging || !updated.temporaryRepackSelected() {
		t.Fatalf("temporary repack was not selected: path=%q selected=%v cmd=%v",
			updated.effectiveUploadPath(), updated.temporaryRepackSelected(), cmd != nil)
	}
	updated.plan = &coreupload.UploadPlan{}
	updated.stage = stageUploading
	updated.startTime = time.Now()
	updated.publishAfterUpload = false
	finished, _ := updated.updateUploading(uploadFinishedMsg{})
	if !cleaned || finished.uploadPath != "" {
		t.Fatalf("temporary repack was not cleaned after upload: cleaned=%v path=%q", cleaned, finished.uploadPath)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("temporary staging still exists after upload: %v", err)
	}
}

func TestTemporaryRepackCanSwitchBackToOriginals(t *testing.T) {
	cleaned := false
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.uploadType = "math"
	model.dirPath = t.TempDir()
	model.uploadPath = t.TempDir()
	model.plan = &coreupload.UploadPlan{}
	model.repackReport = repack.Report{RecommendedFiles: 1}
	model.repackApplyReport = repack.ApplyReport{TemporaryStaging: true, AppliedFiles: 1, UploadDirectory: model.uploadPath}
	model.repackCleanup = func() { cleaned = true }
	model.confirmCursor = model.repackActionIndex()

	updated, cmd := model.updateConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !cleaned || updated.effectiveUploadPath() != updated.dirPath || updated.temporaryRepackSelected() {
		t.Fatalf("switch to originals failed: cleaned=%v path=%q selected=%v cmd=%v",
			cleaned, updated.effectiveUploadPath(), updated.temporaryRepackSelected(), cmd != nil)
	}
}

func TestHeavyMathBooksOfferAnalysisBeforeRunningIt(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.uploadType = "math"
	model.localFiles = []coreupload.LocalFile{{
		AbsPath: "/bundle/books_base.jsonl.zst",
		Size:    repack.DefaultOfferBytes,
	}}
	offered, cmd := model.afterSafetyCheck()
	if cmd != nil || offered.stage != stageRepackOffer {
		t.Fatalf("heavy book did not produce an idle offer: stage=%d cmd=%v", offered.stage, cmd != nil)
	}
	if !strings.Contains(offered.viewRepackOffer(), "significant CPU") {
		t.Fatal("repack offer did not warn about CPU cost")
	}

	skipped, cmd := offered.updateRepackOffer(tea.KeyPressMsg{Code: 'n'})
	if cmd == nil || skipped.stage != stageScanning || skipped.scanPhase != "Checking math compliance" {
		t.Fatalf("skipping analysis did not continue immediately: stage=%d phase=%q", skipped.stage, skipped.scanPhase)
	}
}

func TestSmallMathBooksDoNotOfferRepackAnalysis(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.uploadType = "math"
	model.localFiles = []coreupload.LocalFile{{
		AbsPath: "/bundle/books_base.jsonl.zst",
		Size:    repack.DefaultOfferBytes - 1,
	}}
	next, cmd := model.afterSafetyCheck()
	if cmd == nil || next.stage != stageScanning || next.scanPhase != "Checking math compliance" {
		t.Fatalf("small book was delayed by repack offer: stage=%d phase=%q", next.stage, next.scanPhase)
	}
}

func TestFrontUploadRoutesBeforePlanning(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	t.Setenv("STAKE_ACCELERATOR", "")
	model := New(nil, "team", "game", 120, 40)
	model.uploadType = "front"

	routed, cmd := model.afterSafetyCheck()
	if cmd != nil || routed.stage != stageUploadRoute {
		t.Fatalf("front upload skipped route selection: stage=%d cmd=%v", routed.stage, cmd != nil)
	}
	direct, cmd := routed.updateUploadRoute(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || direct.stage != stageScanning || direct.scanPhase != "Planning upload" || direct.uploadRouteLabel != "Direct to Stake" {
		t.Fatalf("direct route did not continue to planning: stage=%d phase=%q label=%q cmd=%v", direct.stage, direct.scanPhase, direct.uploadRouteLabel, cmd != nil)
	}
}

func TestPassingMathComplianceRoutesBeforePlanning(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	t.Setenv("STAKE_ACCELERATOR", "")
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageCompliance
	model.uploadType = "math"
	model.compliance = coreupload.ComplianceResult{
		Status:      coreupload.CompliancePass,
		GameSummary: coreupload.ComplianceGameSummary{EligibleTiers: []string{"3-star"}},
	}

	routed, cmd := model.updateCompliance(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || routed.stage != stageUploadRoute {
		t.Fatalf("passing compliance skipped route selection: stage=%d cmd=%v", routed.stage, cmd != nil)
	}
}

func TestConfirmViewNeverPresentsComplianceAsPermanentPublishBlock(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.uploadType = "math"
	model.plan = &coreupload.UploadPlan{}
	model.confirmCursor = 2
	view := model.viewConfirm()
	if strings.Contains(view, "blocked") || !strings.Contains(view, "Compliance warning:") || !strings.Contains(view, "> [ Upload ]") {
		t.Fatalf("compliance was presented as a permanent publish block: %s", view)
	}

	updated, cmd := model.updateConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || updated.stage != stageCompliance || !updated.complianceConfirm {
		t.Fatalf("publish did not route to extra acknowledgement: command=%v stage=%d confirm=%v", cmd != nil, updated.stage, updated.complianceConfirm)
	}
}

func TestComplianceViewShowsBothTierVerdictsAndSafeCaps(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageCompliance
	model.compliance = coreupload.ComplianceResult{
		Status:   coreupload.CompliancePass,
		Complete: true,
		TierResults: []coreupload.ComplianceTierResult{
			{Tier: "2-star", MaxCompliantBetLevel: 200, Status: coreupload.CompliancePass, Complete: true, Eligible: true},
			{Tier: "3-star", MaxCompliantBetLevel: 1000, Status: coreupload.CompliancePass, Complete: true, Eligible: true},
		},
	}

	view := model.viewCompliance()
	for _, expected := range []string{
		"2-star: PASS • max compliant bet level 200.00x",
		"3-star: PASS • max compliant bet level 1000.00x",
		"Bet level and currency are excluded from the verdict.",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("tier summary %q missing: %s", expected, view)
		}
	}
}

func TestConfirmOptionsPersistAndApprovalImpliesPublish(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("STAKE_CONFIG_DIR", configDir)

	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.approvalKnown = true
	model.approvalOpen = true
	model.confirmCursor = 0

	withoutPublish, _ := model.updateConfirm(tea.KeyPressMsg{Code: ' ', Text: " "})
	if withoutPublish.publishAfterUpload || withoutPublish.changeApproval {
		t.Fatalf("publish was not disabled: %#v", withoutPublish.preferences)
	}

	withoutPublish.confirmCursor = 1
	withApproval, _ := withoutPublish.updateConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !withApproval.changeApproval || !withApproval.publishAfterUpload {
		t.Fatalf("approval option did not imply publish: %#v", withApproval.preferences)
	}

	reloaded := New(nil, "team", "game", 120, 40)
	if !reloaded.publishAfterUpload || !reloaded.changeApproval {
		t.Fatalf("preferences did not survive a new model: %#v", reloaded.preferences)
	}
	if _, err := os.Stat(filepath.Join(configDir, "upload-preferences.json")); err != nil {
		t.Fatalf("preferences file was not written: %v", err)
	}
}

func TestClosedApprovalOptionIsDisabled(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageConfirm
	model.approvalKnown = true
	model.approvalOpen = false
	model.changeApproval = false
	model.confirmCursor = 1

	updated, _ := model.updateConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated.changeApproval {
		t.Fatal("closed approval option was enabled")
	}
	if view := updated.viewConfirm(); !strings.Contains(view, "unavailable (approval closed)") {
		t.Fatalf("closed approval status missing: %s", view)
	}
}

func TestFailedComplianceRequiresExtraConfirmationBeforeUploadOrPublish(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageCompliance
	model.uploadType = "math"
	model.compliance = coreupload.ComplianceResult{
		Status: coreupload.ComplianceFail, HasError: true, FailuresCount: 1,
		Checks: []coreupload.ComplianceCheck{{
			Name: "RTP", Mode: "base", Status: coreupload.ComplianceFail,
			Details: "RTP 98.0000% exceeds 96.7000%",
		}},
	}

	armed, cmd := model.updateCompliance(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !armed.complianceConfirm || armed.complianceOverride {
		t.Fatalf("first confirmation did not arm override: command=%v confirm=%v override=%v", cmd != nil, armed.complianceConfirm, armed.complianceOverride)
	}
	if view := armed.viewCompliance(); !strings.Contains(view, "RTP 98.0000% exceeds 96.7000%") || !strings.Contains(view, "Continue with upload and publishing anyway? [y/N]") {
		t.Fatalf("compliance details or extra confirmation prompt missing: %s", view)
	}

	confirmed, cmd := armed.updateCompliance(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd != nil || !confirmed.complianceOverride || confirmed.complianceConfirm || confirmed.stage != stageUploadRoute {
		t.Fatalf("override was not confirmed: command=%v confirm=%v override=%v stage=%d", cmd != nil, confirmed.complianceConfirm, confirmed.complianceOverride, confirmed.stage)
	}
}

func TestUploadRouteTUIOffersDirectSavedAndAdd(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	t.Setenv("STAKE_ACCELERATOR", "")
	if err := accelerator.SaveProfile("eu-relay", accelerator.Profile{
		Address: "203.0.113.10:7775", Fingerprint: "ABCDEF",
	}, true); err != nil {
		t.Fatal(err)
	}

	model := New(nil, "team", "game", 120, 40)
	model.uploadType = "front"
	routed, cmd := model.startRouteSelection()
	if cmd != nil || routed.stage != stageUploadRoute {
		t.Fatalf("route screen was not opened: stage=%d cmd=%v", routed.stage, cmd != nil)
	}
	view := routed.viewUploadRoute()
	for _, expected := range []string{
		"Direct to Stake",
		"Accelerator eu-relay",
		"203.0.113.10:7775 • identity pinned • default",
		"Add a new accelerator",
		"Connect, authenticate and pin automatically",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("route option %q missing: %s", expected, view)
		}
	}
}

func TestAddAcceleratorTUICollectsMaskedPassword(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	t.Setenv("STAKE_ACCELERATOR", "")
	model := New(nil, "team", "game", 120, 40)
	model, _ = model.startRouteSelection()
	model.acceleratorRouteCursor = len(model.acceleratorProfileNames) + 1

	form, cmd := model.updateUploadRoute(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || form.stage != stageAcceleratorForm || !form.acceleratorAdding {
		t.Fatalf("add form was not opened: stage=%d adding=%v cmd=%v", form.stage, form.acceleratorAdding, cmd != nil)
	}
	form.acceleratorAddressInput.SetValue("203.0.113.11:7775")
	form.acceleratorNameInput.SetValue("fast-relay")
	form.acceleratorPasswordInput.SetValue("correct horse battery staple")
	form.acceleratorFormFocus = 2
	view := form.viewAcceleratorForm()
	for _, expected := range []string{"Add upload accelerator", "IP or address", "Profile name", "Password", "Connect & pin", "Make default accelerator"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("add form field %q missing: %s", expected, view)
		}
	}
	if strings.Contains(view, "correct horse battery staple") {
		t.Fatal("accelerator password is visible in the TUI")
	}
}

func TestAcceleratorReadyTUIShowsPinAndSpeedTest(t *testing.T) {
	client, err := accelerator.NewClient("203.0.113.12:7775", "correct horse battery staple", "")
	if err != nil {
		t.Fatal(err)
	}
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageAcceleratorBusy
	model.acceleratorBusyReturn = stageAcceleratorForm
	connected, cmd := model.updateAcceleratorBusy(acceleratorConnectedMsg{
		client: client, profileName: "relay", address: "203.0.113.12:7775",
		fingerprint: "AABBCC", newProfile: true,
	})
	if cmd != nil || connected.stage != stageAcceleratorReady {
		t.Fatalf("connected screen was not opened: stage=%d cmd=%v", connected.stage, cmd != nil)
	}
	withSpeed, cmd := connected.updateAcceleratorBusy(acceleratorSpeedTestDoneMsg{result: accelerator.SpeedTestResult{
		Bytes: 64 * 1024 * 1024, Connections: 4, ClientDuration: time.Second, ServerDuration: 500 * time.Millisecond,
	}})
	if cmd != nil || withSpeed.stage != stageAcceleratorReady {
		t.Fatalf("speed result was not accepted: stage=%d cmd=%v", withSpeed.stage, cmd != nil)
	}
	view := withSpeed.viewAcceleratorReady()
	for _, expected := range []string{
		"Certificate pin: sha256:AABBCC",
		"Identity authenticated, pinned and saved locally.",
		"Stake SID is not sent",
		"64.0 MiB/s to relay",
		"128.0 MiB/s receive/decrypt",
		"over 4 connections",
		"Use for this upload",
		"Run 64 MiB speed test",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("connected detail %q missing: %s", expected, view)
		}
	}
}

func TestAcceleratorConnectCanBeCancelledFromTUI(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageAcceleratorBusy
	cancelled := false
	model.acceleratorCancel = func() { cancelled = true }
	model.acceleratorAdding = false

	updated, _ := model.updateAcceleratorBusy(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled || updated.stage != stageAcceleratorForm || updated.acceleratorErr == nil {
		t.Fatalf("busy operation was not cancelled: cancelled=%v stage=%d err=%v", cancelled, updated.stage, updated.acceleratorErr)
	}
}

func TestAcceleratorSpeedTestCancelReturnsToConnectedScreen(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageAcceleratorBusy
	model.acceleratorBusyReturn = stageAcceleratorReady
	cancelled := false
	model.acceleratorCancel = func() { cancelled = true }

	updated, _ := model.updateAcceleratorBusy(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled || updated.stage != stageAcceleratorReady || updated.acceleratorSpeedErr == nil {
		t.Fatalf("speed test cancel lost the connected screen: cancelled=%v stage=%d err=%v", cancelled, updated.stage, updated.acceleratorSpeedErr)
	}
}

func TestAcceleratorDependenciesRemainInjectable(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	if model.connectExistingAccelerator == nil || model.connectNewAccelerator == nil || model.runAcceleratorSpeedTest == nil {
		t.Fatal("accelerator TUI dependencies are not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := model.connectExistingAccelerator(ctx, "missing", "password password"); err == nil {
		t.Fatal("cancelled accelerator connection unexpectedly succeeded")
	}
}

func TestTUIAddAuthenticatesPinsSavesAndSpeedTestsAccelerator(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	password := "correct horse battery staple"
	daemonRoot := t.TempDir()
	configPath := filepath.Join(daemonRoot, "accelerator.json")
	stateDir := filepath.Join(daemonRoot, "state")
	if _, err := accelerator.Initialize(configPath, stateDir, ":0", password); err != nil {
		t.Fatalf("initialize accelerator: %v", err)
	}
	config, err := accelerator.LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := accelerator.NewServer(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewUnstartedServer(server)
	relay.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
	relay.StartTLS()
	defer relay.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, name, err := connectAndSaveAccelerator(ctx, relay.URL, password, "tui-relay", true)
	if err != nil {
		t.Fatalf("TUI add connection: %v", err)
	}
	if name != "tui-relay" || client.Fingerprint() == "" {
		t.Fatalf("unexpected connected profile name=%q fingerprint=%q", name, client.Fingerprint())
	}
	profiles, err := accelerator.LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	profile, exists := profiles.Profiles["tui-relay"]
	if !exists || profiles.Default != "tui-relay" || profile.Fingerprint != client.Fingerprint() {
		t.Fatalf("authenticated pin was not saved: %#v", profiles)
	}
	result, err := client.SpeedTest(ctx, 1024*1024)
	if err != nil || result.Bytes != 1024*1024 {
		t.Fatalf("TUI accelerator speedtest result=%+v err=%v", result, err)
	}

	if _, _, err := connectAndSaveAccelerator(ctx, relay.URL, "wrong password value", "impostor", false); err == nil {
		t.Fatal("wrong password was accepted")
	}
	profiles, err = accelerator.LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := profiles.Profiles["impostor"]; exists {
		t.Fatal("profile was saved before password authentication succeeded")
	}
}

func TestSuccessfulUploadStartsAutomaticPublish(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageUploading
	model.plan = &coreupload.UploadPlan{}
	model.publishAfterUpload = true
	model.startTime = time.Now()

	updated, cmd := model.updateUploading(uploadFinishedMsg{})
	if updated.stage != stageDone || !updated.uploadSucceeded || !updated.publishInFlight {
		t.Fatalf("automatic publish state = stage:%d uploaded:%v publishing:%v", updated.stage, updated.uploadSucceeded, updated.publishInFlight)
	}
	if cmd == nil {
		t.Fatal("automatic publish command was not scheduled")
	}
}

func TestUploadErrorsPreventAutomaticPublish(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageUploading
	model.plan = &coreupload.UploadPlan{}
	model.publishAfterUpload = true
	model.startTime = time.Now()
	model.progressErrors = []string{"upload failed"}

	updated, _ := model.updateUploading(uploadFinishedMsg{})
	if updated.publishInFlight || updated.uploadSucceeded {
		t.Fatalf("publish started after upload failure: uploaded:%v publishing:%v", updated.uploadSucceeded, updated.publishInFlight)
	}
	if updated.errorLogPath != "" {
		defer func() { _ = os.Remove(updated.errorLogPath) }()
	}
}

func TestAutomaticPublishResultUpdatesCombinedStatus(t *testing.T) {
	model := New(nil, "team", "game", 120, 40)
	model.stage = stageDone
	model.uploadType = "math"
	model.uploadSucceeded = true
	model.publishAfterUpload = true
	model.publishInFlight = true
	model.startTime = time.Now().Add(-time.Minute)

	updated, _ := model.updateDone(publishDoneMsg{result: &stake.PublishResult{Version: 7, Changed: true}})
	if !updated.published || updated.err != nil || !strings.Contains(updated.resultMsg, "Upload & publish complete") {
		t.Fatalf("combined publish result = published:%v err:%v message:%q", updated.published, updated.err, updated.resultMsg)
	}

	model.publishInFlight = true
	failed, _ := model.updateDone(publishDoneMsg{err: errTestPublish})
	if failed.publishInFlight || failed.err == nil || !strings.Contains(failed.resultMsg, "publish failed") {
		t.Fatalf("combined publish failure = inFlight:%v err:%v message:%q", failed.publishInFlight, failed.err, failed.resultMsg)
	}
}

type testPublishError string

func (e testPublishError) Error() string { return string(e) }

const errTestPublish testPublishError = "publish unavailable"

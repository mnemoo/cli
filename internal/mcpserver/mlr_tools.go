package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/mlr"
)

type mlrInspectInput struct {
	Path    string  `json:"path" jsonschema:"path to a headerless id,weight,payout CSV LUT"`
	Cost    float64 `json:"cost,omitempty" jsonschema:"mode cost multiplier; defaults to 1"`
	TargetN float64 `json:"target_n" jsonschema:"desired most-likely-result frequency as 1 in N"`
}

type mlrInspectOutput struct {
	Path   string            `json:"path"`
	Result mlr.InspectResult `json:"result"`
}

type mlrChangeInput struct {
	Path    string  `json:"path" jsonschema:"path to a headerless id,weight,payout CSV LUT"`
	Cost    float64 `json:"cost,omitempty" jsonschema:"mode cost multiplier; defaults to 1"`
	TargetN float64 `json:"target_n" jsonschema:"desired most-likely-result frequency as 1 in N"`
	Donor   string  `json:"donor,omitempty" jsonschema:"optional loss, spread, or multiplier range; empty uses automatic least-disturbance selection"`
	RTPLock *bool   `json:"rtp_lock,omitempty" jsonschema:"preserve RTP for manual donor changes; defaults to true"`
}

type mlrApplyInput struct {
	Path    string  `json:"path" jsonschema:"path to a headerless id,weight,payout CSV LUT"`
	Cost    float64 `json:"cost,omitempty" jsonschema:"mode cost multiplier; defaults to 1"`
	TargetN float64 `json:"target_n" jsonschema:"desired most-likely-result frequency as 1 in N"`
	Donor   string  `json:"donor,omitempty" jsonschema:"optional loss, spread, or multiplier range; empty uses automatic least-disturbance selection"`
	RTPLock *bool   `json:"rtp_lock,omitempty" jsonschema:"preserve RTP for manual donor changes; defaults to true"`
	Confirm bool    `json:"confirm" jsonschema:"must be true to overwrite the LUT"`
}

type mlrChangeOutput struct {
	Path             string  `json:"path"`
	Mode             string  `json:"mode"`
	Donor            string  `json:"donor,omitempty"`
	AlreadyMet       bool    `json:"already_met"`
	Feasible         bool    `json:"feasible"`
	Rows             int     `json:"rows"`
	ChangedRows      int     `json:"changed_rows"`
	OddsBefore       float64 `json:"odds_before"`
	PayoutBefore     float64 `json:"payout_before"`
	OddsAfter        float64 `json:"odds_after"`
	PayoutAfter      float64 `json:"payout_after"`
	RTPBefore        float64 `json:"rtp_before"`
	RTPAfter         float64 `json:"rtp_after"`
	RTPDeltaPP       float64 `json:"rtp_delta_pp"`
	RTPLockRequested bool    `json:"rtp_lock_requested"`
	RTPLockHeld      bool    `json:"rtp_lock_held"`
	WeightMovedPct   float64 `json:"weight_moved_pct"`
	LeftoverPct      float64 `json:"leftover_pct"`
	Saved            bool    `json:"saved"`
	BackupPath       string  `json:"backup_path,omitempty"`
}

func (s *Server) registerMLRTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "mlr_inspect", Title: "Inspect LUT MLR",
		Description: "Inspect a LUT's current most-likely result, violating payouts, and donor candidates without changing the file.",
		Annotations: readOnlyAnnotations("Inspect LUT MLR", false),
	}, s.mlrInspect)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "mlr_preview_change", Title: "Preview LUT MLR change",
		Description: "Calculate an automatic or manual MLR reweight and return exact before/after metrics without changing the file. No preview fingerprint is required.",
		Annotations: readOnlyAnnotations("Preview LUT MLR change", false),
	}, s.mlrPreviewChange)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "mlr_change", Title: "Change LUT MLR",
		Description: "Atomically reweight and verify a LUT, retaining a private sibling backup. Requires confirm=true and does not require a preview fingerprint.",
		Annotations: mutationAnnotations("Change LUT MLR", true, false),
	}, s.mlrChange)
}

func (s *Server) mlrInspect(_ context.Context, _ *mcp.CallToolRequest, input mlrInspectInput) (*mcp.CallToolResult, mlrInspectOutput, error) {
	rows, err := mlr.ReadCSV(input.Path)
	if err != nil {
		return nil, mlrInspectOutput{}, err
	}
	result, err := mlr.Inspect(rows, input.Cost, input.TargetN)
	return nil, mlrInspectOutput{Path: input.Path, Result: result}, err
}

func (s *Server) mlrPreviewChange(_ context.Context, _ *mcp.CallToolRequest, input mlrChangeInput) (*mcp.CallToolResult, mlrChangeOutput, error) {
	_, output, err := prepareMLRChange(input.Path, input.Cost, input.TargetN, input.Donor, input.RTPLock)
	return nil, output, err
}

func (s *Server) mlrChange(_ context.Context, _ *mcp.CallToolRequest, input mlrApplyInput) (*mcp.CallToolResult, mlrChangeOutput, error) {
	if !input.Confirm {
		return nil, mlrChangeOutput{}, errors.New("changing a LUT requires confirm=true")
	}
	rows, output, err := prepareMLRChange(input.Path, input.Cost, input.TargetN, input.Donor, input.RTPLock)
	if err != nil || output.AlreadyMet {
		return nil, output, err
	}
	original, err := mlr.ReadCSV(input.Path)
	if err != nil {
		return nil, mlrChangeOutput{}, err
	}
	backup, err := backupLUT(input.Path)
	if err != nil {
		return nil, mlrChangeOutput{}, err
	}
	if err := mlr.WriteCSV(input.Path, rows); err != nil {
		return nil, mlrChangeOutput{}, err
	}
	verified, verifyErr := mlr.ReadCSV(input.Path)
	if verifyErr != nil || !sameRows(rows, verified) {
		_ = mlr.WriteCSV(input.Path, original)
		if verifyErr != nil {
			return nil, mlrChangeOutput{}, fmt.Errorf("verifying saved LUT failed and original was restored: %w", verifyErr)
		}
		return nil, mlrChangeOutput{}, errors.New("saved LUT verification mismatch; original was restored")
	}
	output.Saved = true
	output.BackupPath = backup
	return nil, output, nil
}

func prepareMLRChange(path string, cost, targetN float64, donorRaw string, rtpLock *bool) ([]mlr.Row, mlrChangeOutput, error) {
	rows, err := mlr.ReadCSV(path)
	if err != nil {
		return nil, mlrChangeOutput{}, err
	}
	output := mlrChangeOutput{Path: path, Rows: len(rows), Feasible: true}
	var result mlr.ShiftResult
	if donorRaw == "" {
		output.Mode = "auto"
		auto, autoErr := mlr.Auto(rows, cost, targetN)
		if autoErr != nil {
			return nil, output, autoErr
		}
		output.AlreadyMet = auto.AlreadyMet
		output.Feasible = auto.Feasible || auto.AlreadyMet
		output.Donor = auto.DonorLabel
		if auto.AlreadyMet {
			total, _ := mlr.TotalWeight(rows)
			output.OddsBefore, output.PayoutBefore = mlr.MLR(rows, total)
			output.OddsAfter, output.PayoutAfter = output.OddsBefore, output.PayoutBefore
			output.RTPBefore = mlr.ComputeRTP(rows) / 100 / effectiveCost(cost)
			output.RTPAfter = output.RTPBefore
			return rows, output, nil
		}
		result = auto.Shift
	} else {
		output.Mode = "manual"
		donor, parseErr := mlr.ParseDonor(donorRaw)
		if parseErr != nil {
			return nil, output, parseErr
		}
		output.Donor = donor.String()
		lock := true
		if rtpLock != nil {
			lock = *rtpLock
		}
		result, err = mlr.Shift(rows, cost, targetN, donor, lock)
		if err != nil {
			return nil, output, err
		}
	}
	output.ChangedRows = changedRows(rows, result.Rows)
	output.OddsBefore, output.PayoutBefore = result.OddsBefore, result.PayoutBefore
	output.OddsAfter, output.PayoutAfter = result.OddsAfter, result.PayoutAfter
	output.RTPBefore, output.RTPAfter = result.RTPBefore, result.RTPAfter
	output.RTPDeltaPP = result.RTPDeltaPP()
	output.RTPLockRequested = result.RTPLockRequested
	output.RTPLockHeld = result.RTPLockHeld()
	output.WeightMovedPct = result.WeightMovedPct
	output.LeftoverPct = result.LeftoverPct
	output.Feasible = result.Leftover == 0
	return result.Rows, output, nil
}

func backupLUT(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s.stakecli-%s.bak", filepath.Base(path), time.Now().UTC().Format("20060102T150405.000000000Z"))
	backup := filepath.Join(filepath.Dir(path), name)
	file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating LUT backup: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(backup)
		return "", fmt.Errorf("writing LUT backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(backup)
		return "", fmt.Errorf("syncing LUT backup: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(backup)
		return "", fmt.Errorf("closing LUT backup: %w", err)
	}
	return backup, nil
}

func sameRows(left, right []mlr.Row) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func changedRows(before, after []mlr.Row) int {
	count := 0
	for i := range before {
		if i >= len(after) || before[i] != after[i] {
			count++
		}
	}
	if len(after) > len(before) {
		count += len(after) - len(before)
	}
	return count
}

func effectiveCost(cost float64) float64 {
	if cost > 0 {
		return cost
	}
	return 1
}

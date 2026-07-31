package mlr

import (
	"math"
	"testing"
)

// TestAutoFeasibleHoldsRTP: on a normal LUT, auto hits the target AND keeps
// RTP locked, picking the donor automatically.
func TestAutoFeasibleHoldsRTP(t *testing.T) {
	// MLR is the loss row; ample winning structure + headroom => feasible.
	rows := buildLUT([2]uint64{8_000_000_000, 0})
	rows = repeatRows(rows, 500, 10_000_000, 0)
	rows = repeatRows(rows, 200, 4_000_000, 150)
	rows = repeatRows(rows, 200, 4_000_000, 500)
	const targetN = 60.0

	m, err := Auto(rows, 1, targetN)
	if err != nil {
		t.Fatal(err)
	}
	if m.AlreadyMet {
		t.Fatal("should not already meet target")
	}
	if !m.Feasible {
		t.Fatalf("expected feasible; donor=%q delta=%.4f", m.DonorLabel, m.Shift.RTPDeltaPP())
	}
	if m.DonorLabel == "" {
		t.Error("auto should report the auto-chosen donor")
	}
	if m.Shift.OddsAfter < targetN*0.99 {
		t.Errorf("target not hit: odds=%.3f want >= %.3f", m.Shift.OddsAfter, targetN)
	}
	if !m.Shift.RTPLockHeld() {
		t.Errorf("auto did not hold RTP: Δ %.6f pp", m.Shift.RTPDeltaPP())
	}
	if d := math.Abs(m.Shift.RTPDeltaPP()); d >= RTPLockTolerancePP {
		t.Errorf("RTP drift %.6f pp exceeds tolerance", d)
	}
}

// TestAutoAlreadyMet: if the LUT is already at/under the target, auto does
// nothing.
func TestAutoAlreadyMet(t *testing.T) {
	// Flat LUT: every row equal weight => MLR is already 1 in len(rows).
	rows := repeatRows(nil, 100, 10, 0)
	rows = repeatRows(rows, 100, 10, 200)
	m, err := Auto(rows, 1, 5) // target 1 in 5, far easier than 1 in 200
	if err != nil {
		t.Fatal(err)
	}
	if !m.AlreadyMet {
		t.Errorf("expected AlreadyMet; got donor=%q feasible=%v", m.DonorLabel, m.Feasible)
	}
}

// TestAutoInfeasibleFlags: a degenerate LUT where one outcome dominates cannot
// keep RTP; auto must report Feasible=false and the lock must read not-held.
func TestAutoInfeasibleFlags(t *testing.T) {
	rows := buildLUT([2]uint64{100000, 1000}) // 10x dominates everything
	rows = repeatRows(rows, 5, 1, 0)
	m, err := Auto(rows, 1, 500)
	if err != nil {
		t.Fatal(err)
	}
	if m.AlreadyMet {
		t.Fatal("should not be already met")
	}
	if m.Feasible {
		t.Errorf("expected infeasible (RTP cannot hold); Δ %.4f pp", m.Shift.RTPDeltaPP())
	}
	if m.Shift.RTPLockHeld() {
		t.Error("RTP lock should not read as held on an infeasible shift")
	}
}

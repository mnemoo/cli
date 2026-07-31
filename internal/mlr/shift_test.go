package mlr

import (
	"math"
	"testing"
)

// buildLUT makes rows from (weight, payout) pairs with sequential ids.
func buildLUT(pairs ...[2]uint64) []Row {
	rows := make([]Row, len(pairs))
	for i, p := range pairs {
		rows[i] = Row{ID: uint64(i + 1), Weight: p[0], Payout: p[1]}
	}
	return rows
}

// repeatRows appends n rows of (weight, payout).
func repeatRows(rows []Row, n int, weight, payout uint64) []Row {
	for i := 0; i < n; i++ {
		rows = append(rows, Row{ID: uint64(len(rows) + 1), Weight: weight, Payout: payout})
	}
	return rows
}

func mustShift(t *testing.T, rows []Row, cost, targetN float64, donor Donor, lock bool) ShiftResult {
	t.Helper()
	r, err := Shift(rows, cost, targetN, donor, lock)
	if err != nil {
		t.Fatalf("Shift error: %v", err)
	}
	return r
}

// Realistic LUT weights are large (millions–billions of sim outcomes), so use
// large values here — at weight ~5 the per-row integer rounding swamps the
// signal and is not representative of any real LUT.

// TestShiftHitsTarget: after a feasible shift, the most likely result is no more
// frequent than 1 in N (odds >= target) with negligible overflow.
func TestShiftHitsTarget(t *testing.T) {
	// Dominant loss row + plenty of loss headroom to absorb freed weight.
	rows := buildLUT([2]uint64{5_000_000_000, 0})
	rows = repeatRows(rows, 300, 5_000_000, 0)   // loss headroom
	rows = repeatRows(rows, 100, 3_000_000, 200) // 2x winners
	targetN := 50.0
	r := mustShift(t, rows, 1, targetN, Donor{Kind: DonorLoss}, false)
	if r.LeftoverPct > 0.001 {
		t.Fatalf("unexpected overflow: %.6f%%", r.LeftoverPct)
	}
	if r.OddsAfter < targetN*0.999 {
		t.Errorf("odds after = %.3f, want >= %.3f", r.OddsAfter, targetN)
	}
}

// TestShiftMassConserved: total weight after equals total before minus the
// leftover that did not fit the donor — an exact integer invariant.
func TestShiftMassConserved(t *testing.T) {
	rows := buildLUT([2]uint64{5_000_000_000, 0})
	rows = repeatRows(rows, 300, 5_000_000, 0)
	rows = repeatRows(rows, 100, 3_000_000, 200)
	before, _ := TotalWeight(rows)
	r := mustShift(t, rows, 1, 50, Donor{Kind: DonorLoss}, false)
	after, _ := TotalWeight(r.Rows)
	if after != before-r.Leftover {
		t.Errorf("mass invariant broken: after=%d, before-leftover=%d", after, before-r.Leftover)
	}
}

// TestShiftDoesNotMutateInput: Shift operates on a copy.
func TestShiftDoesNotMutateInput(t *testing.T) {
	rows := buildLUT([2]uint64{5000, 0}, [2]uint64{5, 0}, [2]uint64{5, 0})
	rows = repeatRows(rows, 100, 5, 0)
	snapshot := make([]Row, len(rows))
	copy(snapshot, rows)
	_ = mustShift(t, rows, 1, 10, Donor{Kind: DonorLoss}, false)
	for i := range rows {
		if rows[i] != snapshot[i] {
			t.Fatalf("input mutated at row %d: %+v != %+v", i, rows[i], snapshot[i])
		}
	}
}

// TestBracketingDonorIsRTPNeutral: a bucket bracketing the freed mean payout
// keeps RTP essentially unchanged even without rtp-lock.
func TestBracketingDonorIsRTPNeutral(t *testing.T) {
	// MLR is a 1x row; freed mean = 100 cents. Bucket 0..2x brackets it and has
	// two-sided headroom (rows at 0x and at 2x).
	rows := buildLUT([2]uint64{6_000_000_000, 100})
	rows = repeatRows(rows, 300, 5_000_000, 0)   // 0x with headroom (below mean)
	rows = repeatRows(rows, 300, 5_000_000, 200) // 2x with headroom (above mean)
	r := mustShift(t, rows, 1, 40, Donor{Kind: DonorBucket, Lo: 0, Hi: 2}, false)
	if r.LeftoverPct > 0.001 {
		t.Fatalf("unexpected overflow: %.6f%%", r.LeftoverPct)
	}
	if d := math.Abs(r.RTPDeltaPP()); d > 0.05 {
		t.Errorf("bracketing donor drifted RTP by %.4f pp, want ~0", d)
	}
}

// TestRTPLockHolds: with --rtp-lock on a feasible shift, RTP is pinned to its
// original value (this is the property that matters most).
func TestRTPLockHolds(t *testing.T) {
	// MLR is the loss row (like a real base game); donor=loss keeps the deposit
	// at payout 0, and lock pins any residual via the tail.
	rows := buildLUT([2]uint64{8_000_000_000, 0})
	rows = repeatRows(rows, 500, 10_000_000, 0)  // loss headroom
	rows = repeatRows(rows, 200, 4_000_000, 150) // 1.5x winners
	rows = repeatRows(rows, 200, 4_000_000, 500) // 5x winners (some above mean for the tail)
	r := mustShift(t, rows, 1, 60, Donor{Kind: DonorLoss}, true)
	if !r.RTPLockRequested {
		t.Fatal("RTPLockRequested should be true")
	}
	if !r.RTPLockHeld() {
		t.Errorf("RTP lock did not hold: Δ %.6f pp (want < %.4f)", r.RTPDeltaPP(), RTPLockTolerancePP)
	}
	if d := math.Abs(r.RTPDeltaPP()); d >= RTPLockTolerancePP {
		t.Errorf("RTP delta %.6f pp exceeds tolerance", d)
	}
}

// TestRTPLockInfeasibleFlagged: when the donor saturates (overflow) the lock
// cannot hold; RTPLockHeld must report false rather than silently lying.
func TestRTPLockInfeasibleFlagged(t *testing.T) {
	// One outcome dominates and there is almost nowhere to put the freed weight.
	rows := buildLUT([2]uint64{10000, 300}) // 3x dominant
	rows = repeatRows(rows, 3, 1, 0)        // tiny loss headroom — saturates fast
	r := mustShift(t, rows, 1, 100, Donor{Kind: DonorLoss}, true)
	if r.Leftover == 0 {
		t.Skip("constructed case did not overflow; skipping")
	}
	if r.RTPLockHeld() {
		t.Errorf("RTP lock reported held despite overflow; Δ %.4f pp", r.RTPDeltaPP())
	}
}

func TestShiftValidation(t *testing.T) {
	rows := buildLUT([2]uint64{10, 0}, [2]uint64{5, 100})
	for _, tn := range []float64{1, 0.5, math.Inf(1), math.NaN()} {
		if _, err := Shift(rows, 1, tn, Donor{Kind: DonorLoss}, false); err == nil {
			t.Errorf("target-n=%v: expected error", tn)
		}
	}
	if _, err := Shift(nil, 1, 10, Donor{Kind: DonorLoss}, false); err == nil {
		t.Error("empty rows: expected error")
	}
	if _, err := Shift([]Row{{1, 0, 0}}, 1, 10, Donor{Kind: DonorLoss}, false); err == nil {
		t.Error("all-zero weights: expected error")
	}
}

func TestParseDonor(t *testing.T) {
	cases := []struct {
		in   string
		kind DonorKind
		lo   float64
		hi   float64
		err  bool
	}{
		{"loss", DonorLoss, 0, 0, false},
		{"spread", DonorSpread, 0, 0, false},
		{"proportional", DonorSpread, 0, 0, false},
		{"1-3", DonorBucket, 1, 3, false},
		{"1x-3x", DonorBucket, 1, 3, false},
		{" 0 - 240.28 ", DonorBucket, 0, 240.28, false},
		{"3-1", 0, 0, 0, true},  // hi < lo
		{"-1-3", 0, 0, 0, true}, // negative
		{"abc", 0, 0, 0, true},
		{"", 0, 0, 0, true},
	}
	for _, c := range cases {
		d, err := ParseDonor(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseDonor(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDonor(%q): %v", c.in, err)
			continue
		}
		if d.Kind != c.kind || (c.kind == DonorBucket && (d.Lo != c.lo || d.Hi != c.hi)) {
			t.Errorf("ParseDonor(%q) = %+v, want kind=%v lo=%v hi=%v", c.in, d, c.kind, c.lo, c.hi)
		}
	}
}

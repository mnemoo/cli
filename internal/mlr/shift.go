package mlr

import (
	"fmt"
	"math"
)

// ShiftResult holds the before/after statistics of a Shift, plus the reweighted
// rows (a fresh slice; the input is never mutated).
type ShiftResult struct {
	Rows []Row

	OddsBefore   float64 // "1 in N" most-likely-result odds, before
	PayoutBefore float64 // raw multiplier of the most likely row, before
	OddsAfter    float64
	PayoutAfter  float64

	RTPBefore float64 // RTP ratio (mean payout cents / 100 / cost)
	RTPAfter  float64

	WeightMovedPct float64 // freed weight as a percentage of the original total
	LeftoverPct    float64 // weight that did not fit the donor, as a percentage
	Leftover       uint64  // raw leftover weight (>0 triggers the overflow note)

	RTPLockRequested bool // --rtp-lock was set
}

// RTPDeltaPP is the RTP change in percentage points (after - before).
func (r ShiftResult) RTPDeltaPP() float64 { return (r.RTPAfter - r.RTPBefore) * 100.0 }

// RTPLockTolerancePP is the largest RTP drift (in percentage points) still
// considered "locked". When rtp-lock converges, the residual is many orders of
// magnitude smaller than this; when it is blocked by the cap or by lost mass
// (donor overflow), the drift is far larger — so this threshold sits cleanly in
// the gap between the two regimes.
const RTPLockTolerancePP = 0.01

// RTPLockHeld reports whether --rtp-lock was requested and the resulting RTP
// stayed within RTPLockTolerancePP of the original. It is false when lock was
// not requested. Use RTPLockRequested to distinguish "not asked" from "failed".
func (r ShiftResult) RTPLockHeld() bool {
	return r.RTPLockRequested && abs(r.RTPDeltaPP()) < RTPLockTolerancePP
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Shift performs an MLR shift on a copy of rows: clamp every row's weight at
// floor(total/targetN), then deposit the freed weight into the donor band. With
// rtpLock, RTP is then snapped back to its original value exactly by moving a
// sliver of weight between the tail and the low payouts (maxwin untouched).
//
// targetN must be finite and > 1. cost is used for RTP reporting only.
func Shift(rows []Row, cost, targetN float64, donor Donor, rtpLock bool) (ShiftResult, error) {
	if !(targetN > 1.0 && !math.IsInf(targetN, 0) && !math.IsNaN(targetN)) {
		return ShiftResult{}, fmt.Errorf("target-n must be > 1")
	}
	costEff := cost
	if !(costEff > 0.0) {
		costEff = 1.0
	}
	if len(rows) == 0 {
		return ShiftResult{}, fmt.Errorf("empty LUT")
	}

	out := make([]Row, len(rows))
	copy(out, rows)

	total0, err := TotalWeight(out)
	if err != nil {
		return ShiftResult{}, err
	}
	if total0 == 0 {
		return ShiftResult{}, fmt.Errorf("all weights are zero")
	}

	rtpBefore := ComputeRTP(out) / 100.0 / costEff
	oddsBefore, payoutBefore := MLR(out, total0)

	// Heaviest single row must end up <= total/N.
	cap := uint64(math.Max(math.Floor(float64(total0)/targetN), 1.0))

	var freed uint64
	var freedRTP float64
	for i := range out {
		w := out[i].Weight
		if w > cap {
			rm := w - cap
			freed += rm
			freedRTP += float64(rm) * float64(out[i].Payout)
			out[i].Weight = cap
		}
	}

	placed := placeIntoDonor(out, donor, freed, freedRTP, cap)
	leftover := uint64(0)
	if freed > placed {
		leftover = freed - placed
	}

	if rtpLock {
		totalNow, err := TotalWeight(out)
		if err != nil {
			return ShiftResult{}, err
		}
		targetWP := rtpBefore * 100.0 * costEff * float64(totalNow)
		lockRTP(out, targetWP, cap)
	}

	total1, err := TotalWeight(out)
	if err != nil {
		return ShiftResult{}, err
	}
	rtpAfter := ComputeRTP(out) / 100.0 / costEff
	oddsAfter, payoutAfter := MLR(out, total1)

	return ShiftResult{
		Rows:             out,
		OddsBefore:       oddsBefore,
		PayoutBefore:     payoutBefore,
		OddsAfter:        oddsAfter,
		PayoutAfter:      payoutAfter,
		RTPBefore:        rtpBefore,
		RTPAfter:         rtpAfter,
		WeightMovedPct:   100.0 * float64(freed) / float64(total0),
		LeftoverPct:      100.0 * float64(leftover) / float64(total0),
		Leftover:         leftover,
		RTPLockRequested: rtpLock,
	}, nil
}

// placeIntoDonor deposits `freed` weight — which carried `freedRTP`
// (= sum of removed weight*payout cents) — into the donor rows, keeping every
// row <= cap, while matching that RTP as closely as the donor band allows. When
// the band brackets the removed mean payout this is RTP-neutral (it takes from
// low payouts / gives to high within the band so the mean is unchanged);
// otherwise it does the closest feasible. Returns the weight actually placed
// (< freed if the donor saturates).
func placeIntoDonor(rows []Row, donor Donor, freed uint64, freedRTP float64, cap uint64) uint64 {
	if freed == 0 {
		return 0
	}
	var elig []int
	for i := range rows {
		if donor.eligible(rows[i]) && rows[i].Weight < cap {
			elig = append(elig, i)
		}
	}
	if len(elig) == 0 {
		return 0
	}

	var totalHR float64
	for _, i := range elig {
		totalHR += float64(cap - rows[i].Weight)
	}
	freedF := float64(freed)

	// Not enough headroom to place it all: fill every eligible row to the cap.
	if freedF >= totalHR {
		var placed uint64
		for _, i := range elig {
			placed += cap - rows[i].Weight
			rows[i].Weight = cap
		}
		return placed
	}

	// RTP-neutral split: target mean payout m = freedRTP/freed. Partition donor
	// rows into LOW (payout <= m) and HIGH (payout > m); put weight x into LOW
	// and freed-x into HIGH so the combined mean equals m. Distributing each
	// side proportionally to its headroom makes each side's mean = its
	// headroom-weighted mean, so x = freed*(mHi - m)/(mHi - mLo) hits m exactly
	// (modulo capacity/rounding).
	m := freedRTP / freedF
	var loIdx, hiIdx []int
	var hLo, hHi, spLo, spHi float64
	for _, i := range elig {
		h := float64(cap - rows[i].Weight)
		pp := float64(rows[i].Payout)
		if pp <= m {
			loIdx = append(loIdx, i)
			hLo += h
			spLo += h * pp
		} else {
			hiIdx = append(hiIdx, i)
			hHi += h
			spHi += h * pp
		}
	}
	var mLo, mHi float64
	if hLo > 0.0 {
		mLo = spLo / hLo
	}
	if hHi > 0.0 {
		mHi = spHi / hHi
	}
	var x float64
	switch {
	case len(hiIdx) == 0:
		x = freedF
	case len(loIdx) == 0:
		x = 0.0
	case math.Abs(mHi-mLo) < 1e-9:
		x = freedF * 0.5
	default:
		x = freedF * (mHi - m) / (mHi - mLo)
	}
	x = clamp(x, 0.0, freedF)
	if x > hLo {
		x = hLo
	}
	if freedF-x > hHi {
		x = math.Max(freedF-hHi, 0.0)
	}

	return distribute(rows, loIdx, x, cap) + distribute(rows, hiIdx, freedF-x, cap)
}

// distribute adds `amount` weight across idx proportionally to each row's
// headroom (cap - weight), with the last row absorbing the rounding remainder.
// Returns the integer weight actually added.
func distribute(rows []Row, idx []int, amount float64, cap uint64) uint64 {
	if amount <= 0.0 || len(idx) == 0 {
		return 0
	}
	var hsum float64
	for _, i := range idx {
		hsum += float64(cap - rows[i].Weight)
	}
	if hsum <= 0.0 {
		return 0
	}
	var placed uint64
	var assigned float64
	for k, i := range idx {
		h := float64(cap - rows[i].Weight)
		var add float64
		if k == len(idx)-1 {
			add = math.Max(amount-assigned, 0.0)
		} else {
			add = amount * h / hsum
		}
		add = math.Min(add, h)
		assigned += add
		addU := uint64(math.Max(math.Round(add), 0.0))
		if room := cap - rows[i].Weight; addU > room {
			addU = room
		}
		rows[i].Weight += addU
		placed += addU
	}
	return placed
}

// lockRTP snaps the LUT's RTP back to targetWP (= desired sum of weight*payout
// cents) by shifting weight between the above-mean and below-mean payouts,
// keeping every row <= cap. The correction is spread proportionally across the
// whole pool (pulled ∝ weight, pushed ∝ headroom) so the payout shape is
// preserved and no row is drained to zero. The single global-max (maxwin) payout
// is never touched, so maxwin frequency is preserved.
func lockRTP(rows []Row, targetWP float64, cap uint64) {
	maxPayout := uint64(0)
	var tot float64
	for i := range rows {
		if rows[i].Payout > maxPayout {
			maxPayout = rows[i].Payout
		}
		tot += float64(rows[i].Weight)
	}
	tol := math.Max(tot, 1.0)*1e-9 + 1.0
	for iter := 0; iter < 256; iter++ {
		var curWP float64
		for i := range rows {
			curWP += float64(rows[i].Weight) * float64(rows[i].Payout)
		}
		diff := curWP - targetWP
		if math.Abs(diff) <= tol {
			break
		}
		// diff>0 (RTP too high): pull from above-mean payouts, add to below-mean.
		// diff<0 (RTP too low):  pull from below-mean payouts, add to above-mean.
		wantReduce := diff > 0.0

		// Weighted-mean payout over the movable (non-maxwin) rows.
		var movW, movWP float64
		for i := range rows {
			if rows[i].Payout != maxPayout {
				movW += float64(rows[i].Weight)
				movWP += float64(rows[i].Weight) * float64(rows[i].Payout)
			}
		}
		if movW <= 0.0 {
			break
		}
		mu := movWP / movW

		// Source weighted by weight (what we drain), sink by headroom (what it
		// can absorb).
		var wSrc, wpSrc, hSink, hpSink float64
		for i := range rows {
			if rows[i].Payout == maxPayout {
				continue
			}
			p := float64(rows[i].Payout)
			isSrc := (wantReduce && p > mu) || (!wantReduce && p < mu)
			isSink := (wantReduce && p < mu) || (!wantReduce && p > mu)
			if isSrc && rows[i].Weight > 0 {
				wSrc += float64(rows[i].Weight)
				wpSrc += float64(rows[i].Weight) * p
			} else if isSink && rows[i].Weight < cap {
				h := float64(cap - rows[i].Weight)
				hSink += h
				hpSink += h * p
			}
		}
		if wSrc <= 0.0 || hSink <= 0.0 {
			break
		}
		gap := math.Abs(wpSrc/wSrc - hpSink/hSink)
		if gap < 1e-9 {
			break
		}
		// Weight to move to cancel `diff`, capped by what the pools hold.
		m := math.Round(math.Min(math.Min(math.Abs(diff)/gap, wSrc), hSink))
		if m == 0 {
			break
		}
		mu128 := uint64(m)
		removed := spreadPool(rows, maxPayout, cap, mu, wantReduce, true, mu128)
		if removed == 0 {
			break
		}
		spreadPool(rows, maxPayout, cap, mu, wantReduce, false, removed)
	}
}

// spreadPool moves `amount` weight across one side of the `mu` split, spreading
// it over every eligible non-maxwin row in proportion to its current weight
// (when removing) or its headroom (when adding), so the side's internal shape is
// preserved and no single row is zeroed or pushed past the cap. When `remove`,
// the source side is drained; otherwise the sink side is filled. For
// `wantReduce` the source is the above-mean payouts (sink the below-mean), and
// vice versa. Returns the integer weight actually moved.
func spreadPool(rows []Row, maxPayout, cap uint64, mu float64, wantReduce, remove bool, amount uint64) uint64 {
	if amount == 0 {
		return 0
	}
	var idx []int
	var basis float64
	for i := range rows {
		if rows[i].Payout == maxPayout {
			continue
		}
		p := float64(rows[i].Payout)
		onSrcSide := (wantReduce && p > mu) || (!wantReduce && p < mu)
		onSinkSide := (wantReduce && p < mu) || (!wantReduce && p > mu)
		var eligible bool
		var b float64
		if remove {
			eligible = onSrcSide && rows[i].Weight > 0
			b = float64(rows[i].Weight)
		} else {
			eligible = onSinkSide && rows[i].Weight < cap
			b = float64(cap - rows[i].Weight)
		}
		if eligible {
			basis += b
			idx = append(idx, i)
		}
	}
	if basis <= 0.0 || len(idx) == 0 {
		return 0
	}
	amountF := float64(amount)
	var moved uint64
	var assigned float64
	for k, i := range idx {
		var room uint64
		if remove {
			room = rows[i].Weight
		} else {
			room = cap - rows[i].Weight
		}
		b := float64(room)
		var want float64
		if k == len(idx)-1 {
			want = math.Max(amountF-assigned, 0.0)
		} else {
			want = amountF * b / basis
		}
		assigned += want
		d := uint64(math.Max(math.Round(want), 0.0))
		if d > room {
			d = room
		}
		if remove {
			rows[i].Weight -= d
		} else {
			rows[i].Weight += d
		}
		moved += d
	}
	return moved
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

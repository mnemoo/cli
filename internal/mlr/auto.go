package mlr

import "fmt"

// AutoResult is the outcome of a one-shot Auto shift.
type AutoResult struct {
	AlreadyMet bool        // the LUT already meets the target; nothing was changed
	DonorLabel string      // the donor chosen automatically
	Feasible   bool        // a donor hit the target with no overflow (RTP fully holdable)
	Shift      ShiftResult // the applied shift (rtp-lock always on); zero if AlreadyMet
}

// Auto is the "just give me this MLR" entry point: the caller supplies only the
// desired most-likely-result frequency (1 in targetN); Auto automatically picks
// the donor that hits the target with the least RTP disturbance and no overflow,
// then applies rtp-lock so the RTP is pinned to its original value.
//
// It reuses the same donor preview that `inspect` shows, so its choice is exactly
// the one `inspect` recommends. When no overflow-free donor can reach the target
// (a degenerate LUT where one outcome dominates), Auto falls back to the
// closest-reaching donor, marks Feasible=false, and the caller should surface
// that RTP could not be fully preserved.
func Auto(rows []Row, cost, targetN float64) (AutoResult, error) {
	insp, err := Inspect(rows, cost, targetN)
	if err != nil {
		return AutoResult{}, err
	}
	if insp.AlreadyMet {
		return AutoResult{AlreadyMet: true}, nil
	}
	if len(insp.Candidates) == 0 {
		return AutoResult{}, fmt.Errorf("no donor candidates available for this LUT")
	}

	idx := insp.BestIdx
	feasible := idx >= 0
	if !feasible {
		idx = fallbackDonor(insp.Candidates, targetN)
	}

	label := insp.Candidates[idx].Label
	donor, err := ParseDonor(label)
	if err != nil {
		return AutoResult{}, fmt.Errorf("internal: unparsable donor %q: %w", label, err)
	}
	sr, err := Shift(rows, cost, targetN, donor, true) // rtp-lock always on
	if err != nil {
		return AutoResult{}, err
	}
	return AutoResult{DonorLabel: label, Feasible: feasible, Shift: sr}, nil
}

// fallbackDonor picks the best-effort donor when none cleanly reaches the target
// with zero overflow: prefer candidates that reach the target (odds >= 0.99*N),
// breaking ties by least overflow then least |Δpp|; if none reach the target,
// take the one with the highest resulting odds (closest to the goal).
func fallbackDonor(cands []DonorCandidate, targetN float64) int {
	best := -1
	for i := range cands {
		if cands[i].OddsAfter >= targetN*0.99 {
			if best < 0 || betterEffort(cands[i], cands[best]) {
				best = i
			}
		}
	}
	if best >= 0 {
		return best
	}
	best = 0
	for i := range cands {
		if cands[i].OddsAfter > cands[best].OddsAfter {
			best = i
		}
	}
	return best
}

// betterEffort ranks two target-reaching candidates: less overflow first, then
// smaller RTP disturbance.
func betterEffort(a, b DonorCandidate) bool {
	if a.Overflow != b.Overflow {
		return a.Overflow < b.Overflow
	}
	return abs(a.DeltaPP) < abs(b.DeltaPP)
}

package mlr

import (
	"fmt"
	"math"
	"sort"
)

// ViolValue is one payout value whose heaviest row exceeds the target cap and is
// therefore driving the most-likely-result above the target.
type ViolValue struct {
	PayoutX  float64 // payout multiplier (cents/100)
	NRows    uint32  // number of rows at this payout
	SharePct float64 // 100 * groupWeight / totalWeight
	OddsNow  float64 // 1 in N using the heaviest row at this payout
	OddsUni  float64 // 1 in N if this value's weight were spread evenly over its own rows
}

// DonorCandidate is one previewed --donor option with its simulated outcome.
type DonorCandidate struct {
	Label     string
	OddsAfter float64
	RTPAfter  float64
	DeltaPP   float64 // (rtpAfter - rtpBefore) * 100
	Overflow  float64 // leftover weight as a percentage of the original total
}

// InspectResult is everything needed to render MLR inspection output and pick
// a recommended donor.
type InspectResult struct {
	Rows          int
	UniquePayouts int
	Cost          float64
	RTPBefore     float64
	OddsBefore    float64
	PayoutBefore  float64
	TargetN       float64

	AlreadyMet bool // no value violates the target; nothing to shift

	Viol       []ViolValue // all violating values, heaviest row first
	FreedPct   float64
	Candidates []DonorCandidate
	BestIdx    int // index into Candidates of the recommended donor, or -1
}

// Inspect analyzes a LUT for MLR shifting: current MLR, the values driving it
// above target, and a preview of donor options with their resulting MLR / RTP
// delta — so a caller can pick the smallest-disturbance --donor.
//
// Note: the order of equally-heavy violating values is deterministic
// (heaviest row, then payout as a tie-break), so the output is stable.
func Inspect(rows []Row, cost, targetN float64) (InspectResult, error) {
	if !(targetN > 1.0 && !math.IsInf(targetN, 0) && !math.IsNaN(targetN)) {
		return InspectResult{}, fmt.Errorf("target-n must be > 1")
	}
	costEff := cost
	if !(costEff > 0.0) {
		costEff = 1.0
	}
	if len(rows) == 0 {
		return InspectResult{}, fmt.Errorf("empty LUT")
	}
	total0, err := TotalWeight(rows)
	if err != nil {
		return InspectResult{}, err
	}
	if total0 == 0 {
		return InspectResult{}, fmt.Errorf("all weights are zero")
	}
	tot := float64(total0)
	rtpBefore := ComputeRTP(rows) / 100.0 / costEff
	oddsBefore, payoutBefore := MLR(rows, total0)
	capF := tot / targetN
	capU := uint64(math.Max(math.Floor(capF), 1.0))

	// Aggregate per payout value: (sum weight, row count, heaviest row weight).
	type agg struct {
		w  uint64
		c  uint32
		mx uint64
	}
	byPayout := make(map[uint64]*agg)
	for i := range rows {
		e := byPayout[rows[i].Payout]
		if e == nil {
			e = &agg{}
			byPayout[rows[i].Payout] = e
		}
		e.w += rows[i].Weight
		e.c++
		if rows[i].Weight > e.mx {
			e.mx = rows[i].Weight
		}
	}

	type violRaw struct {
		payout uint64
		w      uint64
		c      uint32
		mx     uint64
	}
	var viol []violRaw
	for p, e := range byPayout {
		if float64(e.mx) > capF {
			viol = append(viol, violRaw{payout: p, w: e.w, c: e.c, mx: e.mx})
		}
	}
	// Heaviest row first; payout ascending as a deterministic tie-break.
	sort.Slice(viol, func(a, b int) bool {
		if viol[a].mx != viol[b].mx {
			return viol[a].mx > viol[b].mx
		}
		return viol[a].payout < viol[b].payout
	})

	var freed uint64
	for i := range rows {
		if rows[i].Weight > capU {
			freed += rows[i].Weight - capU
		}
	}

	res := InspectResult{
		Rows:          len(rows),
		UniquePayouts: len(byPayout),
		Cost:          costEff,
		RTPBefore:     rtpBefore,
		OddsBefore:    oddsBefore,
		PayoutBefore:  payoutBefore,
		TargetN:       targetN,
		FreedPct:      100.0 * float64(freed) / tot,
		BestIdx:       -1,
	}

	if len(viol) == 0 {
		res.AlreadyMet = true
		return res, nil
	}

	minV := math.Inf(1)
	maxV := 0.0
	for _, v := range viol {
		mult := float64(v.payout) / 100.0
		minV = math.Min(minV, mult)
		maxV = math.Max(maxV, mult)
		res.Viol = append(res.Viol, ViolValue{
			PayoutX:  mult,
			NRows:    v.c,
			SharePct: 100.0 * float64(v.w) / tot,
			OddsNow:  tot / float64(v.mx),
			OddsUni:  tot / (float64(v.w) / float64(v.c)),
		})
	}

	// Smallest upper bound U such that the rows in [0, U] can absorb all the
	// freed weight (fill them to the cap) — the RTP-flattest band that won't
	// overflow, since it deposits the freed mass at the lowest payouts available.
	uAbs := computeUAbs(rows, capU, freed, maxV)

	type cand struct {
		label string
		donor Donor
	}
	cands := []cand{
		{"loss", Donor{Kind: DonorLoss}},
		{"spread", Donor{Kind: DonorSpread}},
		{fmt.Sprintf("%.2f-%.2f", minV, maxV*2.0), Donor{Kind: DonorBucket, Lo: minV, Hi: maxV * 2.0}},
		{fmt.Sprintf("0-%.2f", uAbs), Donor{Kind: DonorBucket, Lo: 0.0, Hi: uAbs}},
		{fmt.Sprintf("0-%.2f", math.Max(uAbs*2.0, 1.0)), Donor{Kind: DonorBucket, Lo: 0.0, Hi: math.Max(uAbs*2.0, 1.0)}},
	}
	seen := make(map[string]bool)
	deduped := cands[:0]
	for _, c := range cands {
		if !seen[c.label] {
			seen[c.label] = true
			deduped = append(deduped, c)
		}
	}
	cands = deduped

	bestScore := math.Inf(1)
	for _, c := range cands {
		pv := simulateShift(rows, c.donor, targetN, costEff)
		dpp := (pv.rtpAfter - rtpBefore) * 100.0
		res.Candidates = append(res.Candidates, DonorCandidate{
			Label:     c.label,
			OddsAfter: pv.oddsAfter,
			RTPAfter:  pv.rtpAfter,
			DeltaPP:   dpp,
			Overflow:  pv.leftoverPct,
		})
		if pv.oddsAfter >= targetN*0.99 && pv.leftoverPct < 0.001 {
			score := math.Abs(dpp)
			if score < bestScore {
				bestScore = score
				res.BestIdx = len(res.Candidates) - 1
			}
		}
	}
	return res, nil
}

// computeUAbs returns the smallest payout upper bound (>= maxV) at which the rows
// in [0, U] have enough cumulative headroom (to capU) to absorb all `freed` weight.
func computeUAbs(rows []Row, capU, freed uint64, maxV float64) float64 {
	type pr struct {
		mult float64
		w    uint64
	}
	prs := make([]pr, len(rows))
	for i := range rows {
		prs[i] = pr{mult: float64(rows[i].Payout) / 100.0, w: rows[i].Weight}
	}
	sort.SliceStable(prs, func(a, b int) bool { return prs[a].mult < prs[b].mult })
	u := maxV
	if len(prs) > 0 {
		u = math.Max(prs[len(prs)-1].mult, maxV)
	}
	var cum uint64
	for _, p := range prs {
		if p.w < capU {
			cum += capU - p.w
		}
		if cum >= freed {
			u = math.Max(p.mult, maxV)
			break
		}
	}
	return u
}

// shiftPreview is the simulated outcome of one donor option.
type shiftPreview struct {
	oddsAfter   float64
	rtpAfter    float64
	leftoverPct float64
}

// simulateShift simulates an MLR shift on a copy of rows (clamp at floor(total/N),
// deposit freed weight into donor) and reports the resulting stats — used by
// Inspect. Identical to Shift without rtp-lock and without computing payoutAfter.
func simulateShift(rows []Row, donor Donor, targetN, costEff float64) shiftPreview {
	out := make([]Row, len(rows))
	copy(out, rows)
	total0, _ := TotalWeight(out)
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
	var placed uint64
	if freed > 0 {
		placed = placeIntoDonor(out, donor, freed, freedRTP, cap)
	}
	leftover := uint64(0)
	if freed > placed {
		leftover = freed - placed
	}
	total1, _ := TotalWeight(out)
	rtpAfter := ComputeRTP(out) / 100.0 / costEff
	oddsAfter, _ := MLR(out, total1)
	return shiftPreview{
		oddsAfter:   oddsAfter,
		rtpAfter:    rtpAfter,
		leftoverPct: 100.0 * float64(leftover) / float64(total0),
	}
}

package mlr

import (
	"fmt"
	"strconv"
	"strings"
)

// DonorKind selects where the weight freed by the MLR clamp is deposited.
type DonorKind int

const (
	// DonorLoss deposits into 0x (losing) rows only. RTP can only decrease.
	DonorLoss DonorKind = iota
	// DonorSpread deposits into every row with headroom, proportional to it.
	DonorSpread
	// DonorBucket deposits into rows whose payout multiplier falls in [Lo, Hi].
	DonorBucket
)

// Donor is a parsed donor selection.
type Donor struct {
	Kind DonorKind
	Lo   float64 // inclusive raw-multiplier lower bound (DonorBucket only)
	Hi   float64 // inclusive raw-multiplier upper bound (DonorBucket only)
}

// ParseDonor parses a --donor argument: "loss", "spread"/"proportional", or a
// raw-multiplier range "<minX>-<maxX>" such as "1-3" or "1x-3x".
func ParseDonor(s string) (Donor, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "loss":
		return Donor{Kind: DonorLoss}, nil
	case "spread", "proportional":
		return Donor{Kind: DonorSpread}, nil
	}
	if i := strings.IndexByte(t, '-'); i >= 0 {
		aStr := strings.TrimSpace(t[:i])
		bStr := strings.TrimSpace(t[i+1:])
		aStr = strings.TrimSpace(strings.TrimSuffix(aStr, "x"))
		bStr = strings.TrimSpace(strings.TrimSuffix(bStr, "x"))
		a, errA := strconv.ParseFloat(aStr, 64)
		b, errB := strconv.ParseFloat(bStr, 64)
		if errA == nil && errB == nil && a >= 0.0 && b >= a {
			return Donor{Kind: DonorBucket, Lo: a, Hi: b}, nil
		}
	}
	return Donor{}, fmt.Errorf(
		"invalid --donor %q: use 'loss' | 'spread' | '<minX>-<maxX>' (e.g. 1-3)", s)
}

// String renders the donor the way it appears in command output.
func (d Donor) String() string {
	switch d.Kind {
	case DonorLoss:
		return "loss"
	case DonorSpread:
		return "spread"
	default:
		return fmt.Sprintf("%.2f-%.2f", d.Lo, d.Hi)
	}
}

// eligible reports whether row r can receive freed weight under this donor.
func (d Donor) eligible(r Row) bool {
	switch d.Kind {
	case DonorLoss:
		return r.Payout == 0
	case DonorSpread:
		return true
	default:
		m := float64(r.Payout) / 100.0
		return m >= d.Lo && m <= d.Hi
	}
}

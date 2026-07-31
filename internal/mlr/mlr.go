// Package mlr implements the LUT (look-up table) reweighting primitives behind
// the "MLR shift" operation: read/write a headerless id,weight,payout CSV, and
// surgically shift the Most-Likely-Result frequency by clamping the heaviest
// rows and redistributing the freed weight into a chosen donor band.
//
// It is a pure reweight: no rows are added or removed, ids and payouts stay
// aligned, and only weights change.
package mlr

import (
	"bufio"
	"fmt"
	"math/bits"
	"os"
	"strconv"
)

// Row is a single LUT entry from a headerless CSV: id,weight,payoutMultiplier.
//
// Payout is the payout multiplier in CENTS (100 == 1.00x), matching the LUT
// file format produced by the math engine.
type Row struct {
	ID     uint64
	Weight uint64
	Payout uint64 // payout multiplier in cents (100 = 1x)
}

// u128 is a minimal unsigned 128-bit accumulator (hi:lo) used for exact sums of
// weight*payout. Those sums can exceed uint64 for large LUTs (total_weight up to
// ~1e13, payout up to ~5e5 cents => products near 5e18, sums beyond), which is
// why they are accumulated in 128 bits. Total weight itself is kept in uint64
// with an explicit overflow guard (see TotalWeight).
type u128 struct{ hi, lo uint64 }

// addMul returns a + x*y.
func (a u128) addMul(x, y uint64) u128 {
	hi, lo := bits.Mul64(x, y)
	lo2, carry := bits.Add64(a.lo, lo, 0)
	hi2, _ := bits.Add64(a.hi, hi, carry)
	return u128{hi: hi2, lo: lo2}
}

// float64 converts to float64 as the nearest representable double. For all
// realistic LUTs hi==0, so this equals float64(lo).
func (a u128) float64() float64 {
	return float64(a.hi)*18446744073709551616.0 + float64(a.lo) // hi*2^64 + lo
}

// TotalWeight returns the sum of all row weights. It returns an error if the sum
// overflows uint64 (never the case for real LUTs; the guard is purely defensive).
func TotalWeight(rows []Row) (uint64, error) {
	var total uint64
	for _, r := range rows {
		next := total + r.Weight
		if next < total {
			return 0, fmt.Errorf("total weight overflows uint64 (LUT too large)")
		}
		total = next
	}
	return total, nil
}

// weightedPayout returns the exact 128-bit sum of weight*payout over all rows.
func weightedPayout(rows []Row) u128 {
	var acc u128
	for _, r := range rows {
		acc = acc.addMul(r.Weight, r.Payout)
	}
	return acc
}

// ComputeRTP returns the mean payout in cents: sum(weight*payout)/sum(weight).
// Divide by 100 and by the mode cost to get the RTP ratio. Returns 0 when total
// weight is 0.
func ComputeRTP(rows []Row) float64 {
	total, err := TotalWeight(rows)
	if err != nil || total == 0 {
		return 0.0
	}
	return weightedPayout(rows).float64() / float64(total)
}

// MLR (Most Likely Result) of a LUT is the single heaviest row. It returns the
// "1 in N" odds (total/maxWeight) and the raw payout multiplier of that row
// (payout cents / 100). Returns (0, 0) when there is no weight.
func MLR(rows []Row, total uint64) (oddsN float64, payoutX float64) {
	var mw, mp uint64
	for _, r := range rows {
		if r.Weight > mw {
			mw = r.Weight
			mp = r.Payout
		}
	}
	if mw == 0 || total == 0 {
		return 0.0, 0.0
	}
	return float64(total) / float64(mw), float64(mp) / 100.0
}

// ReadCSV reads a headerless id,weight,payoutMultiplier LUT file. Empty files
// and malformed rows are reported as errors.
func ReadCSV(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var rows []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" {
			continue
		}
		r, err := parseRow(text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CSV file is empty")
	}
	return rows, nil
}

// parseRow parses one "id,weight,payout" line into a Row. Surrounding spaces are
// tolerated; exactly three unsigned-integer fields are required.
func parseRow(text string) (Row, error) {
	var fields [3]string
	n := 0
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == ',' {
			if n < 3 {
				fields[n] = trimSpace(text[start:i])
			}
			n++
			start = i + 1
		}
	}
	if n != 3 {
		return Row{}, fmt.Errorf("expected 3 columns (id,weight,payout), got %d", n)
	}
	id, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return Row{}, fmt.Errorf("invalid id %q: %v", fields[0], err)
	}
	w, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return Row{}, fmt.Errorf("invalid weight %q: %v", fields[1], err)
	}
	p, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return Row{}, fmt.Errorf("invalid payout %q: %v", fields[2], err)
	}
	return Row{ID: id, Weight: w, Payout: p}, nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c == ' ' || c == '\t' || c == '\r' {
			s = s[:len(s)-1]
		} else {
			break
		}
	}
	return s
}

// WriteCSV writes rows back to a headerless id,weight,payout CSV with LF line
// endings and a trailing newline. It writes to a temporary file in the same
// directory and renames it into place so an interrupted write never truncates
// the original LUT.
func WriteCSV(path string, rows []Row) error {
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, ".stakecli-mlr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	w := bufio.NewWriter(tmp)
	buf := make([]byte, 0, 48)
	for _, r := range rows {
		buf = buf[:0]
		buf = strconv.AppendUint(buf, r.ID, 10)
		buf = append(buf, ',')
		buf = strconv.AppendUint(buf, r.Weight, 10)
		buf = append(buf, ',')
		buf = strconv.AppendUint(buf, r.Payout, 10)
		buf = append(buf, '\n')
		if _, err := w.Write(buf); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

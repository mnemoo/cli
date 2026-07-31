// This file implements the stakecli mlr command-line interface: argument
// parsing, dispatch, and human-readable or JSON output rendering.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mnemoo/cli/internal/mlr"
)

const sep = "─────────────────────────────────" // 33 box-drawing chars

// RunMLR parses arguments following `stakecli mlr`, executes the requested
// operation, and returns a process exit code.
func RunMLR(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		printUsage(errOut)
		return 2
	}
	switch args[0] {
	case "shift":
		return runShift(args[1:], out, errOut)
	case "inspect":
		return runInspect(args[1:], out, errOut)
	case "help", "--help", "-h":
		printUsage(out)
		return 0
	default:
		// With no recognized operation, the argument list belongs to the
		// automatic path: `stakecli mlr file.csv --target-n 250`.
		return runAuto(args, out, errOut)
	}
}

// ---- shift ----------------------------------------------------------------

func runShift(args []string, out, errOut io.Writer) int {
	var (
		cost     = 1.0
		targetN  = 0.0
		donorStr = "loss"
		rtpLock  bool
		save     bool
		jsonOut  bool
		help     bool
	)
	hasTarget := false
	rest, err := parseFlags(args,
		map[string]*bool{"rtp-lock": &rtpLock, "save": &save, "json": &jsonOut, "help": &help, "h": &help},
		map[string]func(string) error{
			"cost":     func(s string) error { return parseFloatInto(s, &cost) },
			"target-n": func(s string) error { hasTarget = true; return parseFloatInto(s, &targetN) },
			"donor":    func(s string) error { donorStr = s; return nil },
		})
	if err != nil {
		return fail(errOut, err)
	}
	if help {
		printShiftUsage(out)
		return 0
	}
	file, err := singleFile(rest)
	if err != nil {
		return fail(errOut, err)
	}
	if !hasTarget {
		return fail(errOut, fmt.Errorf("--target-n is required"))
	}
	donor, err := mlr.ParseDonor(donorStr)
	if err != nil {
		return fail(errOut, err)
	}

	rows, err := mlr.ReadCSV(file)
	if err != nil {
		return fail(errOut, err)
	}
	res, err := mlr.Shift(rows, cost, targetN, donor, rtpLock)
	if err != nil {
		return fail(errOut, err)
	}
	if save {
		if err := mlr.WriteCSV(file, res.Rows); err != nil {
			return fail(errOut, err)
		}
	}
	if jsonOut {
		renderShiftJSON(out, file, donorStr, save, res)
	} else {
		renderShiftHuman(out, file, cost, donorStr, save, res)
	}
	return 0
}

func renderShiftHuman(out io.Writer, file string, cost float64, donorStr string, save bool, r mlr.ShiftResult) {
	fmt.Fprintf(out, "MLR shift: %s\n", file)
	fmt.Fprintln(out, sep)
	fmt.Fprintf(out, "Rows:           %d\n", len(r.Rows))
	if cost != 1.0 {
		fmt.Fprintf(out, "Mode cost:      %sx\n", formatCost(cost))
	}
	fmt.Fprintf(out, "Most likely:    1 in %.2f @ %.2fx   ->   1 in %.2f @ %.2fx\n",
		r.OddsBefore, r.PayoutBefore, r.OddsAfter, r.PayoutAfter)
	fmt.Fprintf(out, "RTP:            %.6f (%.2f%%)   ->   %.6f (%.2f%%)   [Δ %+.4f pp]\n",
		r.RTPBefore, r.RTPBefore*100.0, r.RTPAfter, r.RTPAfter*100.0, r.RTPDeltaPP())
	renderRTPLock(out, r)
	fmt.Fprintf(out, "Weight moved:   %.4f%% of total  (donor: %s)\n", r.WeightMovedPct, donorStr)
	if r.Leftover > 0 {
		fmt.Fprintf(out, "Note:           %.4f%% could not fit the donor (its rows hit the cap); total shrank, RTP shifted further.\n", r.LeftoverPct)
	}
	if save {
		fmt.Fprintf(out, "Saved to:       %s\n", file)
	} else {
		fmt.Fprintln(out, "(dry run — pass --save to overwrite the CSV)")
	}
}

func renderShiftJSON(out io.Writer, file, donorStr string, saved bool, r mlr.ShiftResult) {
	type payload struct {
		File           string  `json:"file"`
		Donor          string  `json:"donor"`
		Saved          bool    `json:"saved"`
		Rows           int     `json:"rows"`
		OddsBefore     float64 `json:"odds_before"`
		PayoutBefore   float64 `json:"payout_before"`
		OddsAfter      float64 `json:"odds_after"`
		PayoutAfter    float64 `json:"payout_after"`
		RTPBefore      float64 `json:"rtp_before"`
		RTPAfter       float64 `json:"rtp_after"`
		WeightMovedPct float64 `json:"weight_moved_pct"`
		LeftoverPct    float64 `json:"leftover_pct"`
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload{
		File: file, Donor: donorStr, Saved: saved, Rows: len(r.Rows),
		OddsBefore: r.OddsBefore, PayoutBefore: r.PayoutBefore,
		OddsAfter: r.OddsAfter, PayoutAfter: r.PayoutAfter,
		RTPBefore: r.RTPBefore, RTPAfter: r.RTPAfter,
		WeightMovedPct: r.WeightMovedPct, LeftoverPct: r.LeftoverPct,
	})
}

// renderRTPLock prints the RTP-lock verification line: a clear ✓ when the lock
// held (RTP pinned to its original value) and a loud ✗ when it could not, so a
// silent RTP shift is impossible to miss. Nothing is printed when --rtp-lock was
// not requested.
func renderRTPLock(out io.Writer, r mlr.ShiftResult) {
	if !r.RTPLockRequested {
		return
	}
	if r.RTPLockHeld() {
		fmt.Fprintf(out, "RTP lock:       ✓ held — RTP pinned to %.6f (Δ %+.4f pp)\n", r.RTPBefore, r.RTPDeltaPP())
		return
	}
	fmt.Fprintf(out, "RTP lock:       ✗ NOT held — RTP drifted %+.4f pp; the donor saturated / cap was reached.\n", r.RTPDeltaPP())
	fmt.Fprintln(out, "                Hitting this MLR and keeping RTP are infeasible together here — lower --target-n or widen --donor.")
}

// ---- automatic mode -------------------------------------------------------

func runAuto(args []string, out, errOut io.Writer) int {
	var (
		cost    = 1.0
		targetN = 0.0
		save    bool
		jsonOut bool
		help    bool
	)
	hasTarget := false
	rest, err := parseFlags(args,
		map[string]*bool{"save": &save, "json": &jsonOut, "help": &help, "h": &help},
		map[string]func(string) error{
			"cost":     func(s string) error { return parseFloatInto(s, &cost) },
			"target-n": func(s string) error { hasTarget = true; return parseFloatInto(s, &targetN) },
		})
	if err != nil {
		return fail(errOut, err)
	}
	if help {
		printAutoUsage(out)
		return 0
	}
	file, err := singleFile(rest)
	if err != nil {
		return fail(errOut, err)
	}
	if !hasTarget {
		return fail(errOut, fmt.Errorf("--target-n is required"))
	}
	rows, err := mlr.ReadCSV(file)
	if err != nil {
		return fail(errOut, err)
	}
	res, err := mlr.Auto(rows, cost, targetN)
	if err != nil {
		return fail(errOut, err)
	}
	if save && !res.AlreadyMet {
		if err := mlr.WriteCSV(file, res.Shift.Rows); err != nil {
			return fail(errOut, err)
		}
	}
	if jsonOut {
		renderAutoJSON(out, file, save, res)
	} else {
		renderAutoHuman(out, file, cost, targetN, save, res)
	}
	return 0
}

func renderAutoHuman(out io.Writer, file string, cost, targetN float64, save bool, m mlr.AutoResult) {
	fmt.Fprintf(out, "MLR: %s\n", file)
	fmt.Fprintln(out, sep)
	if m.AlreadyMet {
		fmt.Fprintf(out, "Already at or below target: most likely result is rarer than 1 in %.0f. Nothing to do.\n", targetN)
		return
	}
	r := m.Shift
	fmt.Fprintf(out, "Rows:           %d\n", len(r.Rows))
	if cost != 1.0 {
		fmt.Fprintf(out, "Mode cost:      %sx\n", formatCost(cost))
	}
	fmt.Fprintf(out, "Target:         1 in %.0f\n", targetN)
	fmt.Fprintf(out, "Auto donor:     %s   (chosen automatically for the smallest RTP disturbance)\n", m.DonorLabel)
	fmt.Fprintf(out, "Most likely:    1 in %.2f @ %.2fx   ->   1 in %.2f @ %.2fx\n",
		r.OddsBefore, r.PayoutBefore, r.OddsAfter, r.PayoutAfter)
	fmt.Fprintf(out, "RTP:            %.6f (%.2f%%)   ->   %.6f (%.2f%%)   [Δ %+.4f pp]\n",
		r.RTPBefore, r.RTPBefore*100.0, r.RTPAfter, r.RTPAfter*100.0, r.RTPDeltaPP())
	renderRTPLock(out, r)
	if !m.Feasible {
		fmt.Fprintln(out, "Note:           no overflow-free donor could reach this MLR; applied the closest-reaching donor as best effort.")
		fmt.Fprintln(out, "                Lower --target-n (or split the dominant books) to keep RTP exactly.")
	}
	if save {
		fmt.Fprintf(out, "Saved to:       %s\n", file)
	} else {
		fmt.Fprintln(out, "(dry run — pass --save to overwrite the CSV)")
	}
}

func renderAutoJSON(out io.Writer, file string, saved bool, m mlr.AutoResult) {
	type payload struct {
		File        string  `json:"file"`
		AlreadyMet  bool    `json:"already_met"`
		Donor       string  `json:"donor"`
		Feasible    bool    `json:"feasible"`
		RTPLockHeld bool    `json:"rtp_lock_held"`
		Saved       bool    `json:"saved"`
		Rows        int     `json:"rows"`
		OddsBefore  float64 `json:"odds_before"`
		OddsAfter   float64 `json:"odds_after"`
		RTPBefore   float64 `json:"rtp_before"`
		RTPAfter    float64 `json:"rtp_after"`
		RTPDeltaPP  float64 `json:"rtp_delta_pp"`
		LeftoverPct float64 `json:"leftover_pct"`
	}
	p := payload{File: file, AlreadyMet: m.AlreadyMet, Donor: m.DonorLabel, Feasible: m.Feasible, Saved: saved && !m.AlreadyMet}
	if !m.AlreadyMet {
		r := m.Shift
		p.RTPLockHeld = r.RTPLockHeld()
		p.Rows = len(r.Rows)
		p.OddsBefore, p.OddsAfter = r.OddsBefore, r.OddsAfter
		p.RTPBefore, p.RTPAfter = r.RTPBefore, r.RTPAfter
		p.RTPDeltaPP = r.RTPDeltaPP()
		p.LeftoverPct = r.LeftoverPct
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
}

// ---- inspect --------------------------------------------------------------

func runInspect(args []string, out, errOut io.Writer) int {
	var (
		cost    = 1.0
		targetN = 1000.0
		jsonOut bool
		help    bool
	)
	rest, err := parseFlags(args,
		map[string]*bool{"json": &jsonOut, "help": &help, "h": &help},
		map[string]func(string) error{
			"cost":     func(s string) error { return parseFloatInto(s, &cost) },
			"target-n": func(s string) error { return parseFloatInto(s, &targetN) },
		})
	if err != nil {
		return fail(errOut, err)
	}
	if help {
		printInspectUsage(out)
		return 0
	}
	file, err := singleFile(rest)
	if err != nil {
		return fail(errOut, err)
	}
	rows, err := mlr.ReadCSV(file)
	if err != nil {
		return fail(errOut, err)
	}
	res, err := mlr.Inspect(rows, cost, targetN)
	if err != nil {
		return fail(errOut, err)
	}
	if jsonOut {
		renderInspectJSON(out, file, res)
	} else {
		renderInspectHuman(out, file, res)
	}
	return 0
}

func renderInspectHuman(out io.Writer, file string, r mlr.InspectResult) {
	fmt.Fprintf(out, "MLR inspect: %s\n", file)
	fmt.Fprintln(out, sep)
	fmt.Fprintf(out, "Rows:           %d   Unique payouts: %d\n", r.Rows, r.UniquePayouts)
	if r.Cost != 1.0 {
		fmt.Fprintf(out, "Mode cost:      %sx\n", formatCost(r.Cost))
	}
	fmt.Fprintf(out, "RTP:            %.6f (%.2f%%)\n", r.RTPBefore, r.RTPBefore*100.0)
	fmt.Fprintf(out, "Most likely:    1 in %.2f @ %.2fx\n", r.OddsBefore, r.PayoutBefore)
	fmt.Fprintf(out, "Target:         1 in %.0f\n", r.TargetN)

	if r.AlreadyMet {
		fmt.Fprintf(out, "\nAlready ≥ target: most likely result is rarer than 1 in %.0f. Nothing to shift.\n", r.TargetN)
		return
	}

	fmt.Fprintf(out, "\nValues driving MLR above target  (%d value(s), freed weight = %.3f%%):\n", len(r.Viol), r.FreedPct)
	fmt.Fprintln(out, "  payout       nrows     share%      now        uniform-within")
	for i, v := range r.Viol {
		if i >= 15 {
			break
		}
		fmt.Fprintf(out, "  %8.2fx  %7d  %8.3f%%   1 in %-8.1f 1 in %-8.1f\n",
			v.PayoutX, v.NRows, v.SharePct, v.OddsNow, v.OddsUni)
	}
	if len(r.Viol) > 15 {
		fmt.Fprintf(out, "  ... and %d more\n", len(r.Viol)-15)
	}
	fmt.Fprintln(out, "  (uniform-within = MLR if a value's weight were spread evenly over its OWN rows:")
	fmt.Fprintln(out, "   free, zero RTP/structure change — but only helps if it already reaches the target)")

	fmt.Fprintln(out, "\nDonor preview  (smaller |Δpp| = less RTP disturbance):")
	fmt.Fprintln(out, "  --donor           MLR after      RTP after       Δpp        overflow")
	for _, c := range r.Candidates {
		fmt.Fprintf(out, "  %-15s  1 in %-8.1f  %.4f (%6.2f%%)  %+8.3f   %6.3f%%\n",
			c.Label, c.OddsAfter, c.RTPAfter, c.RTPAfter*100.0, c.DeltaPP, c.Overflow)
	}

	if r.BestIdx >= 0 {
		best := r.Candidates[r.BestIdx]
		fmt.Fprintf(out, "\nRecommended: --donor %s  (hits 1 in %.0f, smallest RTP shift, no overflow)\n", best.Label, r.TargetN)
		fmt.Fprintf(out, "Add --rtp-lock to snap RTP back to %.4f exactly (compensates via the tail).\n", r.RTPBefore)
		fmt.Fprintf(out, "Apply:  stakecli mlr shift '%s' --cost %s --target-n %.0f --donor %s --rtp-lock --save\n",
			file, formatCost(r.Cost), r.TargetN, best.Label)
	} else {
		fmt.Fprintf(out, "\nNo previewed donor reaches 1 in %.0f without overflow — the dominant value(s) carry too much weight to absorb at the single-payout level reweight-only. Options: smaller --target-n, or split those books (adds rows).\n", r.TargetN)
	}
}

func renderInspectJSON(out io.Writer, file string, r mlr.InspectResult) {
	type cand struct {
		Donor     string  `json:"donor"`
		OddsAfter float64 `json:"odds_after"`
		RTPAfter  float64 `json:"rtp_after"`
		DeltaPP   float64 `json:"delta_pp"`
		Overflow  float64 `json:"overflow_pct"`
	}
	type payload struct {
		File          string  `json:"file"`
		Rows          int     `json:"rows"`
		UniquePayouts int     `json:"unique_payouts"`
		RTPBefore     float64 `json:"rtp_before"`
		OddsBefore    float64 `json:"odds_before"`
		PayoutBefore  float64 `json:"payout_before"`
		TargetN       float64 `json:"target_n"`
		AlreadyMet    bool    `json:"already_met"`
		FreedPct      float64 `json:"freed_pct"`
		Candidates    []cand  `json:"candidates"`
		Recommended   *string `json:"recommended_donor"`
	}
	p := payload{
		File: file, Rows: r.Rows, UniquePayouts: r.UniquePayouts,
		RTPBefore: r.RTPBefore, OddsBefore: r.OddsBefore, PayoutBefore: r.PayoutBefore,
		TargetN: r.TargetN, AlreadyMet: r.AlreadyMet, FreedPct: r.FreedPct,
	}
	for _, c := range r.Candidates {
		p.Candidates = append(p.Candidates, cand{c.Label, c.OddsAfter, c.RTPAfter, c.DeltaPP, c.Overflow})
	}
	if r.BestIdx >= 0 {
		rec := r.Candidates[r.BestIdx].Label
		p.Recommended = &rec
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
}

// ---- shared helpers -------------------------------------------------------

// parseFlags is a tiny flag parser supporting "-flag"/"--flag", "--flag=value"
// and "--flag value" forms, with the file positional allowed anywhere. boolFlags
// take no value; valueFlags consume one. Returns the positional arguments.
func parseFlags(args []string, boolFlags map[string]*bool, valueFlags map[string]func(string) error) ([]string, error) {
	var positionals []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) >= 1 && a[0] == '-' && a != "-" {
			name := strings.TrimLeft(a, "-")
			var inlineVal string
			hasInline := false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				inlineVal = name[eq+1:]
				name = name[:eq]
				hasInline = true
			}
			if p, ok := boolFlags[name]; ok {
				if hasInline {
					b, err := strconv.ParseBool(inlineVal)
					if err != nil {
						return nil, fmt.Errorf("invalid value for --%s: %q", name, inlineVal)
					}
					*p = b
				} else {
					*p = true
				}
				i++
				continue
			}
			if setter, ok := valueFlags[name]; ok {
				val := inlineVal
				if !hasInline {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("flag --%s needs a value", name)
					}
					val = args[i+1]
					i++
				}
				if err := setter(val); err != nil {
					return nil, err
				}
				i++
				continue
			}
			return nil, fmt.Errorf("unknown flag %q", a)
		}
		positionals = append(positionals, a)
		i++
	}
	return positionals, nil
}

func parseFloatInto(s string, dst *float64) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("invalid number %q", s)
	}
	*dst = v
	return nil
}

func singleFile(positionals []string) (string, error) {
	switch len(positionals) {
	case 0:
		return "", fmt.Errorf("missing LUT CSV file argument")
	case 1:
		return positionals[0], nil
	default:
		return "", fmt.Errorf("expected exactly one CSV file, got %d: %s", len(positionals), strings.Join(positionals, " "))
	}
}

// formatCost renders a cost/target as the shortest exact decimal: no trailing
// decimals for whole numbers ("250", "1"), shortest round-tripping form otherwise.
func formatCost(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func fail(errOut io.Writer, err error) int {
	fmt.Fprintf(errOut, "Error: %v\n", err)
	return 1
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `stakecli mlr — surgically shift a LUT's Most-Likely-Result by reweighting a CSV.

USAGE:
  stakecli mlr [flags] <file.csv>
  stakecli mlr <command> [flags] <file.csv>

The default operation needs only a target MLR. It chooses the donor and enables
RTP locking automatically:

  stakecli mlr <file.csv> --target-n 250

COMMANDS:
  shift      Clamp the heaviest rows and redistribute the freed weight to hit a
             target MLR (1 in N). Dry-run by default; --save overwrites the CSV.
  inspect    Show the current MLR and a preview of donor options. Read-only.
  help       Print this help.

Run 'stakecli mlr --help' or 'stakecli mlr <command> --help' for flags.
`)
}

func printShiftUsage(w io.Writer) {
	fmt.Fprint(w, `stakecli mlr shift — shift a LUT's Most-Likely-Result to <= 1 in N.

USAGE:
  stakecli mlr shift [flags] <file.csv>

FLAGS:
  --target-n <N>     (required) most likely result must be no more frequent than 1 in N.
  --cost <C>         mode cost, for RTP reporting only (default 1).
  --donor <D>        where freed weight goes (default "loss"):
                       loss          -> 0x rows (RTP decreases)
                       spread        -> every row with headroom, ∝ headroom
                       <minX>-<maxX> -> a raw-multiplier bucket, e.g. "1-3" or "1x-3x"
  --rtp-lock         after redistributing, snap RTP back to its original value exactly.
  --save             overwrite the CSV in place (default: dry-run, report only).
  --json             machine-readable JSON output.
`)
}

func printAutoUsage(w io.Writer) {
	fmt.Fprint(w, `stakecli mlr — hit a target MLR automatically, keeping RTP locked.

Give only the target; stakecli mlr picks the donor that reaches it with the
smallest RTP disturbance and no overflow, then applies --rtp-lock for you.

USAGE:
  stakecli mlr [flags] <file.csv>

FLAGS:
  --target-n <N>     (required) most likely result must be no more frequent than 1 in N.
  --cost <C>         mode cost, for RTP reporting only (default 1).
  --save             overwrite the CSV in place (default: dry-run, report only).
  --json             machine-readable JSON output.
`)
}

func printInspectUsage(w io.Writer) {
	fmt.Fprint(w, `stakecli mlr inspect — preview MLR shifting for a LUT (read-only).

USAGE:
  stakecli mlr inspect [flags] <file.csv>

FLAGS:
  --target-n <N>     most likely result no more frequent than 1 in N (default 1000).
  --cost <C>         mode cost, for RTP reporting only (default 1).
  --json             machine-readable JSON output.
`)
}

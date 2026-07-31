package mlr

import "testing"

func TestInspectAlreadyMet(t *testing.T) {
	rows := repeatRows(nil, 200, 10, 0)
	r, err := Inspect(rows, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !r.AlreadyMet {
		t.Error("expected AlreadyMet")
	}
	if len(r.Viol) != 0 {
		t.Errorf("expected no violations, got %d", len(r.Viol))
	}
}

func TestInspectRecommends(t *testing.T) {
	rows := buildLUT([2]uint64{8000, 0})
	rows = repeatRows(rows, 500, 10, 0)
	rows = repeatRows(rows, 200, 4, 150)
	rows = repeatRows(rows, 200, 4, 500)
	r, err := Inspect(rows, 1, 60)
	if err != nil {
		t.Fatal(err)
	}
	if r.AlreadyMet {
		t.Fatal("should have violations")
	}
	if len(r.Candidates) == 0 {
		t.Fatal("expected donor candidates")
	}
	if r.BestIdx < 0 {
		t.Fatal("expected a recommended donor")
	}
	best := r.Candidates[r.BestIdx]
	// The recommendation must reach the target with no overflow.
	if best.OddsAfter < 60*0.99 {
		t.Errorf("recommended donor misses target: odds=%.3f", best.OddsAfter)
	}
	if best.Overflow >= 0.001 {
		t.Errorf("recommended donor overflows: %.4f%%", best.Overflow)
	}
}

func TestInspectValidation(t *testing.T) {
	rows := buildLUT([2]uint64{10, 0})
	if _, err := Inspect(rows, 1, 1); err == nil {
		t.Error("target-n=1: expected error")
	}
	if _, err := Inspect(nil, 1, 100); err == nil {
		t.Error("empty: expected error")
	}
}

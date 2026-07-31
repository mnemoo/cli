package mlr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRowAndReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lut.csv")
	content := "1,100,0\n2, 200 ,250\n3,0,500000\n" // tolerates surrounding spaces
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{1, 100, 0}, {2, 200, 250}, {3, 0, 500000}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, rows[i], want[i])
		}
	}

	out := filepath.Join(dir, "out.csv")
	if err := WriteCSV(out, rows); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	// Canonical form: no spaces, LF terminators, trailing newline.
	wantBytes := "1,100,0\n2,200,250\n3,0,500000\n"
	if string(got) != wantBytes {
		t.Errorf("WriteCSV bytes:\n got %q\nwant %q", string(got), wantBytes)
	}
}

func TestReadCSVErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty":      "",
		"two cols":   "1,2\n",
		"bad weight": "1,abc,0\n",
		"four cols":  "1,2,3,4\n",
	}
	for name, content := range cases {
		p := filepath.Join(dir, name+".csv")
		os.WriteFile(p, []byte(content), 0o644)
		if _, err := ReadCSV(p); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	if _, err := ReadCSV(filepath.Join(dir, "missing.csv")); err == nil {
		t.Error("missing file: expected error")
	}
}

func TestComputeRTP(t *testing.T) {
	// mean payout = sum(w*p)/sum(w). 100*1 + 0*... etc.
	rows := []Row{{1, 90, 0}, {2, 10, 1000}} // 10% hit at 10x => mean 100 cents = 1.00x
	got := ComputeRTP(rows)
	if got != 100.0 {
		t.Errorf("ComputeRTP = %v, want 100", got)
	}
	if ComputeRTP(nil) != 0 {
		t.Error("ComputeRTP(nil) should be 0")
	}
	if ComputeRTP([]Row{{1, 0, 5}}) != 0 {
		t.Error("ComputeRTP with zero total should be 0")
	}
}

// TestComputeRTPU128 verifies the 128-bit accumulator: these weight*payout
// products sum past uint64, so a naive uint64 sum would wrap and give the wrong
// mean.
func TestComputeRTPU128(t *testing.T) {
	const w = uint64(1e10)
	const p = uint64(1e9) // product 1e19 per row; two rows = 2e19 > maxUint64 (~1.84e19)
	rows := []Row{{1, w, p}, {2, w, p}}
	got := ComputeRTP(rows)
	want := float64(p) // mean of identical payouts is that payout
	if got != want {
		t.Errorf("ComputeRTP (u128 path) = %v, want %v", got, want)
	}
}

func TestTotalWeightOverflow(t *testing.T) {
	rows := []Row{{1, ^uint64(0), 0}, {2, 1, 0}} // maxUint64 + 1 overflows
	if _, err := TotalWeight(rows); err == nil {
		t.Error("expected overflow error")
	}
	if tot, err := TotalWeight([]Row{{1, 5, 0}, {2, 7, 0}}); err != nil || tot != 12 {
		t.Errorf("TotalWeight = %v,%v want 12,nil", tot, err)
	}
}

func TestMLR(t *testing.T) {
	rows := []Row{{1, 50, 0}, {2, 200, 1234}, {3, 100, 99}}
	total, _ := TotalWeight(rows)
	odds, payout := MLR(rows, total)
	if odds != 350.0/200.0 {
		t.Errorf("odds = %v, want %v", odds, 350.0/200.0)
	}
	if payout != 12.34 {
		t.Errorf("payout = %v, want 12.34", payout)
	}
	if o, p := MLR(nil, 0); o != 0 || p != 0 {
		t.Errorf("MLR(nil) = %v,%v want 0,0", o, p)
	}
}

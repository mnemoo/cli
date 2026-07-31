package repack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestEstimateDirectoryFindsExactLevel10SavingsWithoutMutation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "books_base.jsonl.zst")
	plain := testBookData(180_000)
	writeCompressed(t, path, plain, zstd.EncoderLevelFromZstd(1))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)

	report, err := EstimateDirectory(context.Background(), directory, Options{
		Level:             10,
		Workers:           1,
		SampleBytes:       int64(len(plain)) + 1,
		MinSavingsBytes:   1,
		MinSavingsPercent: 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(report.Files))
	}
	estimate := report.Files[0]
	if estimate.Error != "" {
		t.Fatalf("estimate error = %s", estimate.Error)
	}
	if !estimate.Exact {
		t.Fatal("complete book estimate was not marked exact")
	}
	if estimate.EstimatedBytes >= estimate.OriginalBytes {
		t.Fatalf("repack did not improve fixture: original=%d estimated=%d", estimate.OriginalBytes, estimate.EstimatedBytes)
	}
	level10Path := filepath.Join(directory, "expected-level10.zst")
	writeCompressed(t, level10Path, plain, zstd.EncoderLevelFromZstd(10))
	level10Info, err := os.Stat(level10Path)
	if err != nil {
		t.Fatal(err)
	}
	if estimate.EstimatedBytes != level10Info.Size() {
		t.Fatalf("exact estimate=%d, actual level 10=%d", estimate.EstimatedBytes, level10Info.Size())
	}
	if !estimate.Recommended || report.RecommendedFiles != 1 {
		t.Fatalf("recommendation missing: %+v", estimate)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(after); afterHash != beforeHash {
		t.Fatal("read-only estimate mutated the source book")
	}
}

func TestSampledEstimateIsCloseToFullRepack(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "books_bonus.jsonl.zst")
	plain := testBookData(220_000)
	writeCompressed(t, path, plain, zstd.EncoderLevelFromZstd(1))

	report, err := EstimateDirectory(context.Background(), directory, Options{
		Level:             10,
		Workers:           1,
		SampleBytes:       1024 * 1024,
		MinSavingsBytes:   1,
		MinSavingsPercent: 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	estimate := report.Files[0]
	if estimate.Exact {
		t.Fatal("bounded sample was unexpectedly marked exact")
	}
	var actual bytes.Buffer
	encoder, err := zstd.NewWriter(
		&actual,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(10)),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	difference := float64(abs64(estimate.EstimatedBytes-int64(actual.Len()))) / float64(actual.Len())
	if difference > 0.25 {
		t.Fatalf("sample estimate differs from full repack by %.1f%%: estimated=%d actual=%d",
			difference*100, estimate.EstimatedBytes, actual.Len())
	}
}

func TestEstimateDirectoryReportsCorruptBookAndIgnoresOtherFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "books_broken.jsonl.zst"), []byte("not zstd"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := EstimateDirectory(context.Background(), directory, Options{SampleBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 1 || report.Files[0].Error == "" {
		t.Fatalf("corrupt result = %+v", report.Files)
	}
	if report.OriginalBytes != 0 || report.RecommendedFiles != 0 {
		t.Fatalf("corrupt book affected totals: %+v", report)
	}
}

func TestEstimateDirectoryHonorsCancellation(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "books_base.jsonl.zst"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EstimateDirectory(ctx, directory, Options{}); err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestEstimateResourcesUsesSystemTempAndConservativeBounds(t *testing.T) {
	report := Report{OriginalBytes: 300, Workers: 4, Files: []FileEstimate{
		{OriginalBytes: 200, EstimatedBytes: 150, Recommended: true},
		{OriginalBytes: 100, EstimatedBytes: 90, Recommended: false},
	}}
	resources := EstimateResources(report, 500)
	if resources.TemporaryDirectory != os.TempDir() {
		t.Fatalf("temp directory = %q, want %q", resources.TemporaryDirectory, os.TempDir())
	}
	expectedMemory := EstimatedPeakMemoryBytes + 3*EstimatedWorkerMemoryBytes
	if resources.PeakTemporaryBytes < 700 || resources.PeakMemoryBytes != expectedMemory || resources.Workers != 4 {
		t.Fatalf("resource estimate = %+v", resources)
	}
}

func TestNormalizeOptionsUsesBoundedAvailableCPU(t *testing.T) {
	options := normalizeOptions(Options{})
	if options.Workers < 1 || options.Workers > MaxEncoderWorkers {
		t.Fatalf("automatic workers = %d, want 1..%d", options.Workers, MaxEncoderWorkers)
	}
	if options.Workers != RecommendedWorkers() {
		t.Fatalf("automatic workers = %d, recommended = %d", options.Workers, RecommendedWorkers())
	}
	if capped := normalizeOptions(Options{Workers: MaxEncoderWorkers + 100}).Workers; capped != MaxEncoderWorkers {
		t.Fatalf("capped workers = %d, want %d", capped, MaxEncoderWorkers)
	}
}

func testBookData(records int) []byte {
	var output bytes.Buffer
	for index := range records {
		_, _ = fmt.Fprintf(
			&output,
			`{"id":%d,"mode":"base","symbols":[1,2,3,4,5,1,2,3,4,5],"payout":%d,"metadata":"stake-book-repeating-value-%03d"}`+"\n",
			index,
			index%250,
			index%100,
		)
	}
	return output.Bytes()
}

func writeCompressed(t *testing.T, path string, plain []byte, level zstd.EncoderLevel) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(
		file,
		zstd.WithEncoderLevel(level),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := encoder.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

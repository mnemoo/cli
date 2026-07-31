package repack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestApplyRecommendedUsesVerifiedTemporaryBundle(t *testing.T) {
	directory := t.TempDir()
	plain := testBookData(180_000)
	bookPath := filepath.Join(directory, "books_base.jsonl.zst")
	writeCompressed(t, bookPath, plain, zstd.EncoderLevelFromZstd(1))
	if err := os.WriteFile(filepath.Join(directory, "index.json"), []byte(`{"modes":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	options := Options{Level: 10, SampleBytes: int64(len(plain)) + 1, MinSavingsBytes: 1, MinSavingsPercent: 0.01}
	estimate, err := EstimateDirectory(context.Background(), directory, options)
	if err != nil {
		t.Fatal(err)
	}

	report, cleanup, err := ApplyRecommended(context.Background(), directory, estimate, options)
	if err != nil {
		t.Fatal(err)
	}
	staging := report.UploadDirectory
	defer cleanup()
	if report.AppliedFiles != 1 || !report.TemporaryStaging || staging == directory {
		t.Fatalf("apply report = %+v", report)
	}
	relativeToTemp, err := filepath.Rel(os.TempDir(), staging)
	if err != nil || relativeToTemp == "." || relativeToTemp == ".." ||
		strings.HasPrefix(relativeToTemp, ".."+string(filepath.Separator)) {
		t.Fatalf("staging %q is not inside system temp %q", staging, os.TempDir())
	}
	repackedPath := filepath.Join(staging, "books_base.jsonl.zst")
	repackedPlain := decodeBook(t, repackedPath)
	if !bytes.Equal(repackedPlain, plain) {
		t.Fatal("repacked book decompressed content changed")
	}
	sourceInfo, err := os.Stat(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	stagingInfo, err := os.Stat(repackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, stagingInfo) {
		t.Fatal("repacked staging book still points at the source hardlink")
	}
	after, err := os.ReadFile(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(after); afterHash != beforeHash {
		t.Fatal("verified repack mutated the original compressed book")
	}
	index, err := os.ReadFile(filepath.Join(staging, "index.json"))
	if err != nil || string(index) != `{"modes":[]}` {
		t.Fatalf("staging bundle did not preserve index.json: %q %v", index, err)
	}
	cleanup()
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("temporary staging was not cleaned: %v", err)
	}
	if _, err := os.Stat(bookPath); err != nil {
		t.Fatalf("source book was removed with staging: %v", err)
	}
}

func TestApplyRecommendedReportsCompressionAndVerificationProgress(t *testing.T) {
	directory := t.TempDir()
	plain := testBookData(50_000)
	bookPath := filepath.Join(directory, "books_base.jsonl.zst")
	writeCompressed(t, bookPath, plain, zstd.EncoderLevelFromZstd(1))
	options := Options{
		Level: 10, Workers: 2, SampleBytes: int64(len(plain)) + 1,
		MinSavingsBytes: 1, MinSavingsPercent: 0.01,
	}
	estimate, err := EstimateDirectory(context.Background(), directory, options)
	if err != nil {
		t.Fatal(err)
	}
	var events []Progress
	report, cleanup, err := ApplyRecommendedWithProgress(
		context.Background(), directory, estimate, options,
		func(progress Progress) { events = append(events, progress) },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if report.AppliedFiles != 1 {
		t.Fatalf("apply report = %+v", report)
	}
	var sawRepack, sawVerify, sawComplete bool
	for _, event := range events {
		if event.Workers != 2 {
			t.Fatalf("progress workers = %d, want 2", event.Workers)
		}
		switch event.Phase {
		case ProgressRepacking:
			sawRepack = true
		case ProgressVerifying:
			sawVerify = true
		case ProgressComplete:
			sawComplete = true
		}
	}
	if !sawRepack || !sawVerify || !sawComplete {
		t.Fatalf("missing progress phases: repack=%v verify=%v complete=%v events=%+v", sawRepack, sawVerify, sawComplete, events)
	}
}

func TestApplyRecommendedKeepsOriginalWhenActualThresholdIsNotMet(t *testing.T) {
	directory := t.TempDir()
	plain := testBookData(50_000)
	bookPath := filepath.Join(directory, "books_base.jsonl.zst")
	writeCompressed(t, bookPath, plain, zstd.EncoderLevelFromZstd(1))
	estimateOptions := Options{Level: 10, SampleBytes: int64(len(plain)) + 1, MinSavingsBytes: 1, MinSavingsPercent: 0.01}
	estimate, err := EstimateDirectory(context.Background(), directory, estimateOptions)
	if err != nil {
		t.Fatal(err)
	}
	if estimate.RecommendedFiles != 1 {
		t.Fatalf("fixture was not recommended: %+v", estimate)
	}

	report, cleanup, err := ApplyRecommended(context.Background(), directory, estimate, Options{
		Level: 10, MinSavingsBytes: 1 << 60, MinSavingsPercent: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if report.AppliedFiles != 0 || report.UploadDirectory != directory || report.TemporaryStaging {
		t.Fatalf("non-beneficial apply report = %+v", report)
	}
	if !bytes.Equal(decodeBook(t, bookPath), plain) {
		t.Fatal("source book changed when actual threshold was not met")
	}
}

func TestApplyRecommendedRejectsEstimateFromAnotherDirectory(t *testing.T) {
	directory := t.TempDir()
	_, _, err := ApplyRecommended(context.Background(), directory, Report{
		Directory:        t.TempDir(),
		RecommendedFiles: 1,
	}, Options{})
	if err == nil {
		t.Fatal("estimate from another directory was accepted")
	}
}

func TestApplyRecommendedRejectsUnsafeOrNonBookCandidate(t *testing.T) {
	directory := t.TempDir()
	for _, candidate := range []string{"../books_escape.jsonl.zst", "index.json"} {
		_, _, err := ApplyRecommended(context.Background(), directory, Report{
			Directory:        directory,
			RecommendedFiles: 1,
			Files: []FileEstimate{{
				Path:        candidate,
				Recommended: true,
			}},
		}, Options{})
		if err == nil {
			t.Fatalf("candidate %q was accepted", candidate)
		}
	}
}

func TestBundleSizeMatchesFilesIncludedInTemporaryMirror(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.json"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".DS_Store"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "node_modules", "ignored"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	size, err := BundleSize(directory)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Fatalf("bundle size = %d, want 5", size)
	}
}

func TestStaleTemporaryRepackCleanupIsScopedAndAgeGuarded(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, stagingPrefix+"stale")
	fresh := filepath.Join(root, stagingPrefix+"fresh")
	unrelated := filepath.Join(root, "other-stale-directory")
	for _, path := range []string{stale, fresh, unrelated} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	old := now.Add(-staleStagingMaxAge - time.Hour)
	for _, path := range []string{stale, unrelated} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStaleStaging(root, now)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale repack directory still exists: %v", err)
	}
	for _, path := range []string{fresh, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("guarded directory %q was removed: %v", path, err)
		}
	}
}

func decodeBook(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	plain, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

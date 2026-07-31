package repack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

type AppliedFile struct {
	Path            string        `json:"path"`
	OriginalBytes   int64         `json:"original_bytes"`
	RepackedBytes   int64         `json:"repacked_bytes"`
	SavingsBytes    int64         `json:"savings_bytes"`
	SavingsPercent  float64       `json:"savings_percent"`
	UncompressedSHA string        `json:"uncompressed_sha256"`
	Duration        time.Duration `json:"duration"`
	Applied         bool          `json:"applied"`
}

type ApplyReport struct {
	SourceDirectory  string        `json:"source_directory"`
	UploadDirectory  string        `json:"upload_directory"`
	Files            []AppliedFile `json:"files"`
	AppliedFiles     int           `json:"applied_files"`
	OriginalBytes    int64         `json:"original_bytes"`
	RepackedBytes    int64         `json:"repacked_bytes"`
	SavingsBytes     int64         `json:"savings_bytes"`
	Duration         time.Duration `json:"duration"`
	TemporaryStaging bool          `json:"temporary_staging"`
}

type ProgressPhase string

const (
	ProgressPreparing ProgressPhase = "preparing"
	ProgressRepacking ProgressPhase = "repacking"
	ProgressVerifying ProgressPhase = "verifying"
	ProgressComplete  ProgressPhase = "complete"
)

// Progress describes the current full-repack pass. Bytes and Total refer to
// compressed input bytes, which keeps progress measurable without buffering
// decompressed JSONL in memory or on disk.
type Progress struct {
	Phase     ProgressPhase
	Path      string
	FileIndex int
	FileCount int
	Bytes     int64
	Total     int64
	Workers   int
}

type ProgressFunc func(Progress)

const (
	stagingPrefix      = "stakecli-repack-"
	staleStagingMaxAge = 24 * time.Hour
)

func (r ApplyReport) SavingsPercent() float64 {
	if r.OriginalBytes <= 0 {
		return 0
	}
	return float64(r.SavingsBytes) / float64(r.OriginalBytes) * 100
}

// ApplyRecommended creates a temporary mirror of the bundle and repacks only
// files recommended by the estimator. Original files are never opened for
// writing. The returned cleanup must be called after upload/publish finishes.
func ApplyRecommended(ctx context.Context, directory string, estimate Report, options Options) (ApplyReport, func(), error) {
	return ApplyRecommendedWithProgress(ctx, directory, estimate, options, nil)
}

// ApplyRecommendedWithProgress is ApplyRecommended with throttled progress
// events for the compression and verification passes.
func ApplyRecommendedWithProgress(
	ctx context.Context,
	directory string,
	estimate Report,
	options Options,
	progress ProgressFunc,
) (ApplyReport, func(), error) {
	if options.Workers <= 0 && estimate.Workers > 0 {
		options.Workers = estimate.Workers
	}
	options = normalizeOptions(options)
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return ApplyReport{}, nil, err
	}
	report := ApplyReport{SourceDirectory: absolute, UploadDirectory: absolute}
	if estimate.RecommendedFiles == 0 {
		return report, func() {}, nil
	}
	estimateRoot, err := filepath.Abs(estimate.Directory)
	if err != nil || estimateRoot != absolute {
		return ApplyReport{}, nil, errors.New("repack estimate does not belong to the selected bundle")
	}

	started := time.Now()
	emitProgress(progress, Progress{
		Phase: ProgressPreparing, FileCount: estimate.RecommendedFiles, Workers: options.Workers,
	})
	cleanupStaleStaging(os.TempDir(), time.Now())
	staging, err := os.MkdirTemp("", stagingPrefix+"*")
	if err != nil {
		return ApplyReport{}, nil, fmt.Errorf("creating repack staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	if err := mirrorBundle(ctx, absolute, staging); err != nil {
		cleanup()
		return ApplyReport{}, nil, err
	}

	fileIndex := 0
	for _, candidate := range estimate.Files {
		if !candidate.Recommended {
			continue
		}
		fileIndex++
		relative, err := safeRelative(candidate.Path)
		if err != nil {
			cleanup()
			return ApplyReport{}, nil, err
		}
		if !IsBookPath(relative) {
			cleanup()
			return ApplyReport{}, nil, fmt.Errorf("repack candidate is not a supported book: %q", candidate.Path)
		}
		applied, err := repackOne(
			ctx,
			filepath.Join(absolute, relative),
			filepath.Join(staging, relative),
			candidate.Path,
			options,
			fileIndex,
			estimate.RecommendedFiles,
			progress,
		)
		if err != nil {
			cleanup()
			return ApplyReport{}, nil, fmt.Errorf("repacking %s: %w", candidate.Path, err)
		}
		report.Files = append(report.Files, applied)
		if applied.Applied {
			report.AppliedFiles++
			report.OriginalBytes += applied.OriginalBytes
			report.RepackedBytes += applied.RepackedBytes
			report.SavingsBytes += applied.SavingsBytes
		}
	}
	report.Duration = time.Since(started)
	emitProgress(progress, Progress{
		Phase: ProgressComplete, FileIndex: fileIndex, FileCount: estimate.RecommendedFiles, Workers: options.Workers,
	})
	if report.AppliedFiles == 0 {
		cleanup()
		report.UploadDirectory = absolute
		return report, func() {}, nil
	}
	report.UploadDirectory = staging
	report.TemporaryStaging = true
	return report, cleanup, nil
}

// cleanupStaleStaging removes only abandoned StakeCLI repack directories.
// Normal flows clean immediately; this catches leftovers after a hard crash or
// power loss without touching a possibly active repack from the last 24 hours.
func cleanupStaleStaging(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleStagingMaxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

func repackOne(
	ctx context.Context,
	sourcePath, stagingPath, displayPath string,
	options Options,
	fileIndex, fileCount int,
	progress ProgressFunc,
) (AppliedFile, error) {
	result := AppliedFile{Path: displayPath}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return result, err
	}
	result.OriginalBytes = info.Size()
	started := time.Now()

	source, err := os.Open(sourcePath)
	if err != nil {
		return result, err
	}
	compressedSource := newProgressReader(
		&contextReader{ctx: ctx, reader: source},
		Progress{Phase: ProgressRepacking, Path: displayPath, FileIndex: fileIndex, FileCount: fileCount, Total: info.Size(), Workers: options.Workers},
		progress,
	)
	decoder, err := newBoundedDecoder(compressedSource)
	if err != nil {
		_ = source.Close()
		return result, err
	}
	temp, err := os.CreateTemp(filepath.Dir(stagingPath), ".repacked-*.zst")
	if err != nil {
		decoder.Close()
		_ = source.Close()
		return result, err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		decoder.Close()
		_ = source.Close()
		return result, err
	}
	encoder, err := zstd.NewWriter(
		temp,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(options.Level)),
		zstd.WithEncoderConcurrency(options.Workers),
		zstd.WithLowerEncoderMem(true),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		_ = temp.Close()
		decoder.Close()
		_ = source.Close()
		return result, err
	}
	originalHash := sha256.New()
	_, copyErr := io.CopyBuffer(encoder, io.TeeReader(decoder, originalHash), make([]byte, 1024*1024))
	encoderErr := encoder.Close()
	decoder.Close()
	sourceErr := source.Close()
	syncErr := temp.Sync()
	tempErr := temp.Close()
	for _, candidate := range []error{copyErr, encoderErr, sourceErr, syncErr, tempErr} {
		if candidate != nil {
			return result, candidate
		}
	}
	compressedSource.finish()

	repackedInfo, err := os.Stat(tempPath)
	if err != nil {
		return result, err
	}
	result.RepackedBytes = repackedInfo.Size()
	result.SavingsBytes = result.OriginalBytes - result.RepackedBytes
	result.SavingsPercent = float64(result.SavingsBytes) / float64(result.OriginalBytes) * 100

	repackedHash, err := decompressedHash(
		ctx,
		tempPath,
		Progress{Phase: ProgressVerifying, Path: displayPath, FileIndex: fileIndex, FileCount: fileCount, Total: repackedInfo.Size(), Workers: options.Workers},
		progress,
	)
	if err != nil {
		return result, fmt.Errorf("verifying repacked stream: %w", err)
	}
	if !bytes.Equal(originalHash.Sum(nil), repackedHash) {
		return result, errors.New("repacked decompressed SHA-256 does not match the source")
	}
	result.UncompressedSHA = fmt.Sprintf("%x", repackedHash)
	result.Duration = time.Since(started)
	if result.SavingsBytes < options.MinSavingsBytes || result.SavingsPercent < options.MinSavingsPercent {
		return result, nil
	}
	if err := os.Remove(stagingPath); err != nil {
		return result, err
	}
	if err := os.Rename(tempPath, stagingPath); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

func decompressedHash(ctx context.Context, path string, state Progress, progress ProgressFunc) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	tracked := newProgressReader(&contextReader{ctx: ctx, reader: file}, state, progress)
	decoder, err := newBoundedDecoder(tracked)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, decoder, make([]byte, 1024*1024)); err != nil {
		return nil, err
	}
	tracked.finish()
	return hash.Sum(nil), nil
}

const progressInterval = 100 * time.Millisecond

type progressReader struct {
	reader   io.Reader
	state    Progress
	progress ProgressFunc
	last     time.Time
}

func newProgressReader(reader io.Reader, state Progress, progress ProgressFunc) *progressReader {
	tracked := &progressReader{reader: reader, state: state, progress: progress}
	emitProgress(progress, state)
	tracked.last = time.Now()
	return tracked
}

func (r *progressReader) Read(output []byte) (int, error) {
	n, err := r.reader.Read(output)
	r.state.Bytes += int64(n)
	now := time.Now()
	if n > 0 && (now.Sub(r.last) >= progressInterval || r.state.Bytes >= r.state.Total) {
		emitProgress(r.progress, r.state)
		r.last = now
	}
	return n, err
}

func (r *progressReader) finish() {
	if r.state.Total > 0 {
		r.state.Bytes = r.state.Total
	}
	emitProgress(r.progress, r.state)
}

func emitProgress(progress ProgressFunc, state Progress) {
	if progress != nil {
		progress(state)
	}
}

func newBoundedDecoder(reader io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(
		reader,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(maxDecoderMemory),
		zstd.WithDecoderMaxWindow(maxDecoderWindow),
	)
}

func mirrorBundle(ctx context.Context, sourceRoot, stagingRoot string) error {
	return filepath.WalkDir(sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && skippedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && skippedFile(entry.Name()) {
			return nil
		}
		destination := filepath.Join(stagingRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return copyResolvedFile(sourcePath, destination)
		}
		if err := os.Link(sourcePath, destination); err == nil {
			return nil
		}
		return copyResolvedFile(sourcePath, destination)
	})
}

// BundleSize returns the bytes that may need to be mirrored when the system
// temp directory is on another filesystem and hard links are unavailable.
func BundleSize(directory string) (int64, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != absolute && skippedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.IsDir() || skippedFile(entry.Name()) {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measuring repack bundle: %w", err)
	}
	return total, nil
}

func copyResolvedFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(output, input, make([]byte, 1024*1024))
	syncErr := output.Sync()
	closeErr := output.Close()
	for _, candidate := range []error{copyErr, syncErr, closeErr} {
		if candidate != nil {
			_ = os.Remove(destination)
			return candidate
		}
	}
	return nil
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", "__pycache__":
		return true
	default:
		return false
	}
}

func skippedFile(name string) bool {
	return name == ".DS_Store" || name == "Thumbs.db" || strings.HasPrefix(name, ".")
}

func safeRelative(path string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(path))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe repack path %q", path)
	}
	return cleaned, nil
}

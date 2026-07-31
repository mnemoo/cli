package repack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	DefaultLevel                      = 10
	DefaultSampleBytes         int64  = 64 * 1024 * 1024
	DefaultMinSavingsBytes            = 16 * 1024 * 1024
	DefaultMinSavingsPercent          = 5.0
	DefaultOfferBytes          int64  = 100 * 1024 * 1024
	EstimatedPeakMemoryBytes   int64  = 384 * 1024 * 1024
	EstimatedWorkerMemoryBytes int64  = 96 * 1024 * 1024
	MaxEncoderWorkers                 = 8
	maxCompressedRead                 = 32 * 1024
	maxDecoderMemory           uint64 = 256 * 1024 * 1024
	maxDecoderWindow           uint64 = 128 * 1024 * 1024
)

type Options struct {
	Level             int
	Workers           int
	SampleBytes       int64
	MinSavingsBytes   int64
	MinSavingsPercent float64
}

type FileEstimate struct {
	Path                      string        `json:"path"`
	OriginalBytes             int64         `json:"original_bytes"`
	EstimatedBytes            int64         `json:"estimated_bytes"`
	EstimatedSavingsBytes     int64         `json:"estimated_savings_bytes"`
	EstimatedSavingsPercent   float64       `json:"estimated_savings_percent"`
	SampledUncompressedBytes  int64         `json:"sampled_uncompressed_bytes"`
	SampledOriginalBytes      int64         `json:"sampled_original_bytes"`
	SampledRecompressedBytes  int64         `json:"sampled_recompressed_bytes"`
	EstimatedUncompressedSize int64         `json:"estimated_uncompressed_bytes"`
	SampleDuration            time.Duration `json:"sample_duration"`
	EstimatedRepackDuration   time.Duration `json:"estimated_repack_duration"`
	Exact                     bool          `json:"exact"`
	Recommended               bool          `json:"recommended"`
	Error                     string        `json:"error,omitempty"`
}

type Report struct {
	Directory                 string         `json:"directory"`
	Level                     int            `json:"level"`
	Workers                   int            `json:"workers"`
	SampleBytes               int64          `json:"sample_bytes"`
	Files                     []FileEstimate `json:"files"`
	OriginalBytes             int64          `json:"original_bytes"`
	EstimatedBytes            int64          `json:"estimated_bytes"`
	EstimatedSavingsBytes     int64          `json:"estimated_savings_bytes"`
	RecommendedFiles          int            `json:"recommended_files"`
	RecommendedOriginalBytes  int64          `json:"recommended_original_bytes"`
	RecommendedEstimatedBytes int64          `json:"recommended_estimated_bytes"`
	RecommendedSavingsBytes   int64          `json:"recommended_savings_bytes"`
	Duration                  time.Duration  `json:"duration"`
}

type ResourceEstimate struct {
	TemporaryDirectory string `json:"temporary_directory"`
	PeakTemporaryBytes int64  `json:"peak_temporary_bytes"`
	PeakMemoryBytes    int64  `json:"peak_memory_bytes"`
	Workers            int    `json:"workers"`
}

// EstimateResources returns conservative bounds for the temporary repack.
// Books are processed sequentially while each encoder uses several workers.
// The temp bound assumes the bundle mirror must be copied instead of hard-linked
// and that the largest replacement exists beside its source copy.
func EstimateResources(report Report, bundleBytes int64) ResourceEstimate {
	if bundleBytes < report.OriginalBytes {
		bundleBytes = report.OriginalBytes
	}
	var largestReplacement int64
	for _, file := range report.Files {
		if !file.Recommended {
			continue
		}
		candidate := max(file.OriginalBytes, file.EstimatedBytes)
		if candidate > largestReplacement {
			largestReplacement = candidate
		}
	}
	const filesystemHeadroom = int64(16 * 1024 * 1024)
	workers := report.Workers
	if workers <= 0 {
		workers = RecommendedWorkers()
	}
	return ResourceEstimate{
		TemporaryDirectory: os.TempDir(),
		PeakTemporaryBytes: max(int64(0), bundleBytes) + largestReplacement + filesystemHeadroom,
		PeakMemoryBytes:    EstimatedPeakMemoryBytes + int64(workers-1)*EstimatedWorkerMemoryBytes,
		Workers:            workers,
	}
}

// RecommendedWorkers uses the available Go CPU budget while bounding the
// encoder's memory usage. Books are still processed one at a time.
func RecommendedWorkers() int {
	return min(max(1, runtime.GOMAXPROCS(0)), MaxEncoderWorkers)
}

func (r Report) SavingsPercent() float64 {
	if r.OriginalBytes <= 0 {
		return 0
	}
	return float64(r.EstimatedSavingsBytes) / float64(r.OriginalBytes) * 100
}

func (r Report) RecommendedSavingsPercent() float64 {
	if r.RecommendedOriginalBytes <= 0 {
		return 0
	}
	return float64(r.RecommendedSavingsBytes) / float64(r.RecommendedOriginalBytes) * 100
}

func EstimateDirectory(ctx context.Context, directory string, options Options) (Report, error) {
	options = normalizeOptions(options)
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Report{}, fmt.Errorf("resolving repack path: %w", err)
	}
	paths, err := findBooks(absolute)
	if err != nil {
		return Report{}, err
	}
	report := Report{Directory: absolute, Level: options.Level, Workers: options.Workers, SampleBytes: options.SampleBytes}
	started := time.Now()
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		estimate := estimateFile(ctx, absolute, path, options)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		report.Files = append(report.Files, estimate)
		if estimate.Error != "" {
			continue
		}
		report.OriginalBytes += estimate.OriginalBytes
		report.EstimatedBytes += estimate.EstimatedBytes
		report.EstimatedSavingsBytes += estimate.EstimatedSavingsBytes
		if estimate.Recommended {
			report.RecommendedFiles++
			report.RecommendedOriginalBytes += estimate.OriginalBytes
			report.RecommendedEstimatedBytes += estimate.EstimatedBytes
			report.RecommendedSavingsBytes += estimate.EstimatedSavingsBytes
		}
	}
	report.Duration = time.Since(started)
	return report, nil
}

func normalizeOptions(options Options) Options {
	if options.Level == 0 {
		options.Level = DefaultLevel
	}
	if options.Workers <= 0 {
		options.Workers = RecommendedWorkers()
	} else {
		options.Workers = min(options.Workers, MaxEncoderWorkers)
	}
	if options.SampleBytes <= 0 {
		options.SampleBytes = DefaultSampleBytes
	}
	if options.MinSavingsBytes <= 0 {
		options.MinSavingsBytes = DefaultMinSavingsBytes
	}
	if options.MinSavingsPercent <= 0 {
		options.MinSavingsPercent = DefaultMinSavingsPercent
	}
	return options
}

func findBooks(directory string) ([]string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("reading repack path: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("repack path must be a directory")
	}
	var paths []string
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != directory && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if IsBookPath(entry.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("finding Zstd books: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func IsBookPath(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(name, "books") && strings.HasSuffix(name, ".jsonl.zst")
}

func estimateFile(ctx context.Context, root, path string, options Options) FileEstimate {
	result := FileEstimate{Path: relativePath(root, path)}
	info, err := os.Stat(path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.OriginalBytes = info.Size()
	file, err := os.Open(path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = file.Close() }()

	countedInput := &countingReader{reader: &contextReader{ctx: ctx, reader: file}}
	decoder, err := zstd.NewReader(
		countedInput,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(maxDecoderMemory),
		zstd.WithDecoderMaxWindow(maxDecoderWindow),
	)
	if err != nil {
		result.Error = fmt.Sprintf("opening Zstd stream: %v", err)
		return result
	}
	defer decoder.Close()

	compressed := &countingWriter{}
	encoder, err := zstd.NewWriter(
		compressed,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(options.Level)),
		zstd.WithEncoderConcurrency(options.Workers),
		zstd.WithLowerEncoderMem(true),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		result.Error = fmt.Sprintf("creating Zstd level %d estimator: %v", options.Level, err)
		return result
	}

	started := time.Now()
	sampled, copyErr := io.CopyBuffer(
		encoder,
		io.LimitReader(decoder, options.SampleBytes),
		make([]byte, 1024*1024),
	)
	closeErr := encoder.Close()
	result.SampleDuration = time.Since(started)
	if copyErr != nil {
		result.Error = fmt.Sprintf("sampling Zstd stream: %v", copyErr)
		return result
	}
	if closeErr != nil {
		result.Error = fmt.Sprintf("closing Zstd estimator: %v", closeErr)
		return result
	}
	if sampled <= 0 || countedInput.count <= 0 {
		result.Error = "Zstd stream did not contain any book data"
		return result
	}

	result.SampledUncompressedBytes = sampled
	result.SampledOriginalBytes = countedInput.count
	result.SampledRecompressedBytes = compressed.count
	result.Exact = sampled < options.SampleBytes && countedInput.count >= info.Size()
	if result.Exact {
		result.EstimatedBytes = compressed.count
		result.EstimatedUncompressedSize = sampled
		result.EstimatedRepackDuration = result.SampleDuration
	} else {
		newRatio := float64(compressed.count) / float64(countedInput.count)
		result.EstimatedBytes = max(1, int64(math.Round(float64(info.Size())*newRatio)))
		expansion := float64(sampled) / float64(countedInput.count)
		result.EstimatedUncompressedSize = max(sampled, int64(math.Round(float64(info.Size())*expansion)))
		result.EstimatedRepackDuration = time.Duration(
			float64(result.SampleDuration) * float64(result.EstimatedUncompressedSize) / float64(sampled),
		)
	}
	result.EstimatedSavingsBytes = result.OriginalBytes - result.EstimatedBytes
	result.EstimatedSavingsPercent = float64(result.EstimatedSavingsBytes) / float64(result.OriginalBytes) * 100
	result.Recommended = result.EstimatedSavingsBytes >= options.MinSavingsBytes &&
		result.EstimatedSavingsPercent >= options.MinSavingsPercent
	return result
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(relative)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(output []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(output)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(output []byte) (int, error) {
	if len(output) > maxCompressedRead {
		output = output[:maxCompressedRead]
	}
	n, err := r.reader.Read(output)
	r.count += int64(n)
	return n, err
}

type countingWriter struct {
	count int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	w.count += int64(len(data))
	return len(data), nil
}

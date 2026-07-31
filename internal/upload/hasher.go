package upload

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const (
	// MultipartThreshold follows the Stake Engine API contract: files above
	// 100 MiB use multipart upload, smaller files use a single PUT.
	MultipartThreshold int64 = 100 * 1024 * 1024
	// PartSize is deliberately smaller than the multipart threshold so a large
	// math book can keep several S3 connections busy without buffering huge
	// chunks in memory.
	PartSize int64 = 8 * 1024 * 1024
	// ChunkSize is kept as a compatibility alias for callers that use the API
	// threshold. Multipart hashing and upload offsets use PartSize instead.
	ChunkSize = MultipartThreshold
)

type PartHash struct {
	Number int
	Size   int64
	MD5    [16]byte
	ETag   string // quoted hex, e.g. "\"abcdef...\""
}

type ETagResult struct {
	ETag  string     // quoted ETag for the whole file
	Parts []PartHash // non-nil only for multipart
}

// ComputeETag calculates the S3-compatible ETag for a file.
// Small files (<=MultipartThreshold): simple MD5.
// Large files: composite MD5 over PartSize chunks plus the part count suffix.
func ComputeETag(path string, fileSize int64) (*ETagResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if fileSize <= MultipartThreshold {
		return computeSingleETag(f)
	}
	return computeMultipartETag(f, fileSize)
}

func computeSingleETag(r io.Reader) (*ETagResult, error) {
	h := md5.New()
	if _, err := io.Copy(h, r); err != nil {
		return nil, fmt.Errorf("hashing file: %w", err)
	}
	hexStr := hex.EncodeToString(h.Sum(nil))
	return &ETagResult{
		ETag: fmt.Sprintf(`"%s"`, hexStr),
	}, nil
}

func computeMultipartETag(r io.Reader, fileSize int64) (*ETagResult, error) {
	var parts []PartHash
	partNum := 1
	remaining := fileSize

	for remaining > 0 {
		chunkLen := PartSize
		if remaining < chunkLen {
			chunkLen = remaining
		}

		h := md5.New()
		n, err := io.CopyN(h, r, chunkLen)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("hashing part %d: %w", partNum, err)
		}

		var digest [16]byte
		copy(digest[:], h.Sum(nil))
		hexStr := hex.EncodeToString(digest[:])

		parts = append(parts, PartHash{
			Number: partNum,
			Size:   n,
			MD5:    digest,
			ETag:   fmt.Sprintf(`"%s"`, hexStr),
		})

		remaining -= n
		partNum++
	}

	// Composite ETag: MD5 of concatenated binary MD5s + "-N"
	composite := md5.New()
	for _, p := range parts {
		composite.Write(p.MD5[:])
	}
	compositeHex := hex.EncodeToString(composite.Sum(nil))
	etag := fmt.Sprintf(`"%s-%d"`, compositeHex, len(parts))

	return &ETagResult{
		ETag:  etag,
		Parts: parts,
	}, nil
}

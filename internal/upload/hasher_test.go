package upload

import "testing"

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestMultipartETagUses8MiBParts(t *testing.T) {
	const eightMiB = 8 * 1024 * 1024
	if PartSize != eightMiB {
		t.Fatalf("PartSize = %d, want %d", PartSize, eightMiB)
	}
	result, err := computeMultipartETag(zeroReader{}, PartSize+17)
	if err != nil {
		t.Fatalf("computeMultipartETag: %v", err)
	}
	if len(result.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(result.Parts))
	}
	if result.Parts[0].Size != PartSize || result.Parts[1].Size != 17 {
		t.Fatalf("part sizes = %d, %d", result.Parts[0].Size, result.Parts[1].Size)
	}
}

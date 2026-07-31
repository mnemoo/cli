package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/mnemoo/cli/internal/api"
)

func TestMultipartETagMatchesCompositeMD5(t *testing.T) {
	first := md5.Sum([]byte("first part"))
	second := md5.Sum([]byte("second part"))
	parts := []api.FileCompletePartBody{
		{Number: 1, ETag: `"` + hex.EncodeToString(first[:]) + `"`, Size: 10},
		{Number: 2, ETag: `"` + hex.EncodeToString(second[:]) + `"`, Size: 11},
	}

	composite := md5.New()
	_, _ = composite.Write(first[:])
	_, _ = composite.Write(second[:])
	want := fmt.Sprintf(`"%s-2"`, hex.EncodeToString(composite.Sum(nil)))

	got, err := multipartETag(parts)
	if err != nil {
		t.Fatalf("multipartETag() error = %v", err)
	}
	if got != want {
		t.Fatalf("multipartETag() = %q, want %q", got, want)
	}
}

func TestMultipartETagRejectsInvalidParts(t *testing.T) {
	tests := []struct {
		name  string
		parts []api.FileCompletePartBody
	}{
		{name: "empty"},
		{name: "not hex", parts: []api.FileCompletePartBody{{Number: 1, ETag: `"invalid"`}}},
		{name: "wrong length", parts: []api.FileCompletePartBody{{Number: 1, ETag: `"00"`}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := multipartETag(test.parts); err == nil {
				t.Fatal("multipartETag() unexpectedly succeeded")
			}
		})
	}
}

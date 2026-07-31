package accelerator

import (
	"bytes"
	"io"
	"testing"
)

func TestEncryptedPayloadRoundTrip(t *testing.T) {
	authKey := bytes.Repeat([]byte{0x42}, 32)
	taskID := "abcdefghijklmnopqrstuvwx"
	metadata := []byte(`{"url":"https://example.com/put","size":12}`)
	payload := bytes.Repeat([]byte("streamed-data-"), 200_000)

	var encrypted bytes.Buffer
	var counted int64
	if err := writeEncryptedPayload(&encrypted, authKey, taskID, metadata, bytes.NewReader(payload), func(delta int64) {
		counted += delta
	}); err != nil {
		t.Fatalf("writeEncryptedPayload: %v", err)
	}
	if counted != int64(len(payload)) {
		t.Fatalf("counted plaintext = %d, want %d", counted, len(payload))
	}
	if bytes.Contains(encrypted.Bytes(), payload[:64]) || bytes.Contains(encrypted.Bytes(), metadata) {
		t.Fatal("encrypted spool contains plaintext")
	}
	if int64(encrypted.Len()) != encryptedStreamSize(int64(len(metadata)), int64(len(payload))) {
		t.Fatalf("encrypted size = %d, want %d", encrypted.Len(), encryptedStreamSize(int64(len(metadata)), int64(len(payload))))
	}

	decryptor, err := newDecryptReader(bytes.NewReader(encrypted.Bytes()), authKey, taskID)
	if err != nil {
		t.Fatalf("newDecryptReader: %v", err)
	}
	gotMetadata, err := decryptor.nextFrame()
	if err != nil {
		t.Fatalf("metadata frame: %v", err)
	}
	if !bytes.Equal(gotMetadata, metadata) {
		t.Fatalf("metadata mismatch: %q", gotMetadata)
	}
	gotPayload, err := io.ReadAll(decryptor)
	if err != nil {
		t.Fatalf("decrypt payload: %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("payload mismatch after decrypt")
	}
}

func TestEncryptedPayloadRejectsTampering(t *testing.T) {
	authKey := bytes.Repeat([]byte{0x33}, 32)
	taskID := "abcdefghijklmnopqrstuvwx"
	var encrypted bytes.Buffer
	if err := writeEncryptedPayload(&encrypted, authKey, taskID, []byte(`{"size":4}`), bytes.NewBufferString("data"), nil); err != nil {
		t.Fatal(err)
	}
	data := encrypted.Bytes()
	data[len(data)-6] ^= 0xff
	decryptor, err := newDecryptReader(bytes.NewReader(data), authKey, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptor.nextFrame(); err != nil {
		t.Fatalf("metadata should remain valid: %v", err)
	}
	if _, err := io.ReadAll(decryptor); err == nil {
		t.Fatal("tampered payload was accepted")
	}
}

func TestEncryptedPayloadUsesBoundedStreamingReads(t *testing.T) {
	reader := &boundedReader{remaining: 5*streamPlainChunkSize + 17}
	authKey := bytes.Repeat([]byte{0x19}, 32)
	if err := writeEncryptedPayload(io.Discard, authKey, "abcdefghijklmnopqrstuvwx", []byte(`{"size":1}`), reader, nil); err != nil {
		t.Fatal(err)
	}
	if reader.maximumRequest > streamPlainChunkSize {
		t.Fatalf("maximum source read = %d, want <= %d", reader.maximumRequest, streamPlainChunkSize)
	}
	if reader.remaining != 0 {
		t.Fatalf("stream did not consume payload: %d bytes remain", reader.remaining)
	}
}

func TestEncryptedPayloadIsDeterministicForResume(t *testing.T) {
	authKey := bytes.Repeat([]byte{0x28}, 32)
	taskID := "resume-session-task-id"
	metadata := []byte(`{"size":14}`)
	payload := []byte("resumable data")
	var first, second bytes.Buffer
	if err := writeEncryptedPayload(&first, authKey, taskID, metadata, bytes.NewReader(payload), nil); err != nil {
		t.Fatal(err)
	}
	if err := writeEncryptedPayload(&second, authKey, taskID, metadata, bytes.NewReader(payload), nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("same task did not reproduce the same authenticated ciphertext")
	}
}

func BenchmarkEncryptedStreamThroughput(b *testing.B) {
	authKey := bytes.Repeat([]byte{0x77}, 32)
	payload := bytes.Repeat([]byte{0x5a}, 64*1024*1024)
	metadata := []byte(`{"url":"https://example.com/upload","size":67108864}`)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := writeEncryptedPayload(io.Discard, authKey, "abcdefghijklmnopqrstuvwx", metadata, bytes.NewReader(payload), nil); err != nil {
			b.Fatal(err)
		}
	}
}

type boundedReader struct {
	remaining      int
	maximumRequest int
}

func (r *boundedReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.maximumRequest {
		r.maximumRequest = len(buffer)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(buffer), r.remaining)
	for i := range buffer[:n] {
		buffer[i] = byte(i)
	}
	r.remaining -= n
	return n, nil
}

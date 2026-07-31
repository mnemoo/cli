package accelerator

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	streamMagic          = "SAC1"
	streamPlainChunkSize = 1024 * 1024
	pbkdf2Iterations     = 600_000
)

// DeriveAuthKey turns the user-supplied password into a server-specific key.
// The certificate fingerprint is public salt: changing the server identity
// also changes the password verifier and all payload keys.
func DeriveAuthKey(password string, certificateFingerprint []byte) ([]byte, error) {
	if len(password) < 12 {
		return nil, errors.New("accelerator password must contain at least 12 characters")
	}
	return pbkdf2.Key(sha256.New, password, certificateFingerprint, pbkdf2Iterations, 32)
}

func payloadKey(authKey []byte, taskID string) []byte {
	mac := hmac.New(sha256.New, authKey)
	_, _ = mac.Write([]byte("stake-accelerator/payload/v1\x00"))
	_, _ = mac.Write([]byte(taskID))
	return mac.Sum(nil)
}

func requestMAC(authKey []byte, method, path, timestamp, nonce string) string {
	mac := hmac.New(sha256.New, authKey)
	_, _ = mac.Write([]byte(method))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(path))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validRequestMAC(authKey []byte, method, path, timestamp, nonce, supplied string) bool {
	expected := requestMAC(authKey, method, path, timestamp, nonce)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func serverProof(authKey []byte, nonce string) string {
	mac := hmac.New(sha256.New, authKey)
	_, _ = mac.Write([]byte("stake-accelerator/server-proof/v1\x00"))
	_, _ = mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validServerProof(authKey []byte, nonce, supplied string) bool {
	expected := serverProof(authKey, nonce)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type encryptWriter struct {
	writer      io.Writer
	aead        cipher.AEAD
	taskID      string
	noncePrefix [4]byte
	counter     uint64
	scratch     []byte
}

func newEncryptWriter(writer io.Writer, authKey []byte, taskID string) (*encryptWriter, error) {
	key := payloadKey(authKey, taskID)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	result := &encryptWriter{writer: writer, aead: aead, taskID: taskID}
	prefixMAC := hmac.New(sha256.New, key)
	_, _ = prefixMAC.Write([]byte("stake-accelerator/nonce-prefix/v1"))
	copy(result.noncePrefix[:], prefixMAC.Sum(nil))
	if _, err := io.WriteString(writer, streamMagic); err != nil {
		return nil, err
	}
	if _, err := writer.Write(result.noncePrefix[:]); err != nil {
		return nil, err
	}
	return result, nil
}

func (w *encryptWriter) writeFrame(plain []byte) error {
	nonce := w.nonce()
	ciphertext := w.aead.Seal(w.scratch[:0], nonce, plain, w.associatedData())
	if len(ciphertext) > int(^uint32(0)) {
		return errors.New("accelerator frame is too large")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(ciphertext)))
	if _, err := w.writer.Write(length[:]); err != nil {
		return err
	}
	if _, err := w.writer.Write(ciphertext); err != nil {
		return err
	}
	w.scratch = ciphertext[:0]
	w.counter++
	return nil
}

func (w *encryptWriter) close() error {
	var end [4]byte
	_, err := w.writer.Write(end[:])
	return err
}

func (w *encryptWriter) nonce() []byte {
	nonce := make([]byte, w.aead.NonceSize())
	copy(nonce, w.noncePrefix[:])
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], w.counter)
	return nonce
}

func (w *encryptWriter) associatedData() []byte {
	data := make([]byte, len(w.taskID)+8)
	copy(data, w.taskID)
	binary.BigEndian.PutUint64(data[len(w.taskID):], w.counter)
	return data
}

type decryptReader struct {
	reader      io.Reader
	aead        cipher.AEAD
	taskID      string
	noncePrefix [4]byte
	counter     uint64
	current     []byte
	ciphertext  []byte
	plaintext   []byte
	done        bool
}

func newDecryptReader(reader io.Reader, authKey []byte, taskID string) (*decryptReader, error) {
	var header [8]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, fmt.Errorf("reading accelerator stream header: %w", err)
	}
	if string(header[:4]) != streamMagic {
		return nil, errors.New("invalid accelerator stream header")
	}
	block, err := aes.NewCipher(payloadKey(authKey, taskID))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	result := &decryptReader{reader: reader, aead: aead, taskID: taskID}
	copy(result.noncePrefix[:], header[4:])
	return result, nil
}

func (r *decryptReader) nextFrame() ([]byte, error) {
	if r.done {
		return nil, io.EOF
	}
	var length [4]byte
	if _, err := io.ReadFull(r.reader, length[:]); err != nil {
		return nil, fmt.Errorf("reading accelerator frame length: %w", err)
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 {
		r.done = true
		return nil, io.EOF
	}
	if size > streamPlainChunkSize+uint32(r.aead.Overhead())+64*1024 {
		return nil, fmt.Errorf("accelerator frame exceeds limit: %d", size)
	}
	if cap(r.ciphertext) < int(size) {
		r.ciphertext = make([]byte, size)
	} else {
		r.ciphertext = r.ciphertext[:size]
	}
	ciphertext := r.ciphertext
	if _, err := io.ReadFull(r.reader, ciphertext); err != nil {
		return nil, fmt.Errorf("reading accelerator frame: %w", err)
	}
	nonce := make([]byte, r.aead.NonceSize())
	copy(nonce, r.noncePrefix[:])
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], r.counter)
	associated := make([]byte, len(r.taskID)+8)
	copy(associated, r.taskID)
	binary.BigEndian.PutUint64(associated[len(r.taskID):], r.counter)
	plain, err := r.aead.Open(r.plaintext[:0], nonce, ciphertext, associated)
	if err != nil {
		return nil, errors.New("accelerator frame authentication failed")
	}
	r.counter++
	r.plaintext = plain[:0]
	return plain, nil
}

func (r *decryptReader) Read(output []byte) (int, error) {
	for len(r.current) == 0 {
		frame, err := r.nextFrame()
		if err != nil {
			return 0, err
		}
		r.current = frame
	}
	n := copy(output, r.current)
	r.current = r.current[n:]
	return n, nil
}

func encryptedStreamSize(metadataSize, payloadSize int64) int64 {
	frameSize := func(size int64) int64 {
		if size <= 0 {
			return 0
		}
		return 4 + size + 16
	}
	total := int64(8) + frameSize(metadataSize) + 4
	full := payloadSize / streamPlainChunkSize
	remainder := payloadSize % streamPlainChunkSize
	total += full * frameSize(streamPlainChunkSize)
	total += frameSize(remainder)
	return total
}

func writeEncryptedPayload(writer io.Writer, authKey []byte, taskID string, metadata []byte, body io.Reader, onRead func(int64)) error {
	encryptor, err := newEncryptWriter(writer, authKey, taskID)
	if err != nil {
		return err
	}
	if err := encryptor.writeFrame(metadata); err != nil {
		return err
	}
	buffer := make([]byte, streamPlainChunkSize)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if onRead != nil {
				onRead(int64(n))
			}
			if err := encryptor.writeFrame(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}
	return encryptor.close()
}

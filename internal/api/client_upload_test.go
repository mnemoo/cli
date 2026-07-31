package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUploadToS3StreamsReaderAndReportsBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != "streamed" {
			t.Errorf("body = %q", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("")
	var read int64
	err := client.UploadToS3(context.Background(), server.URL, strings.NewReader("streamed"), 8, "text/plain", func(delta int64) {
		read += delta
	}, nil)
	if err != nil {
		t.Fatalf("UploadToS3: %v", err)
	}
	if read != 8 {
		t.Fatalf("reported bytes = %d, want 8", read)
	}
}

func TestUploadToS3ReportsBodyWrittenBeforeResponse(t *testing.T) {
	allowResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-allowResponse
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("")
	bodySent := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.UploadToS3(
			context.Background(), server.URL, strings.NewReader("payload"), 7, "",
			nil, func() { bodySent <- struct{}{} },
		)
	}()

	select {
	case <-bodySent:
	case <-time.After(time.Second):
		t.Fatal("body-written callback did not fire before the response")
	}
	select {
	case err := <-done:
		t.Fatalf("upload returned before response was allowed: %v", err)
	default:
	}
	close(allowResponse)
	if err := <-done; err != nil {
		t.Fatalf("UploadToS3: %v", err)
	}
}

func TestS3TransportPipelineSettings(t *testing.T) {
	client := NewClient("")
	transport, ok := client.s3http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.s3http.Transport)
	}
	if transport.MaxConnsPerHost != s3MaxConnections {
		t.Fatalf("MaxConnsPerHost = %d, want %d", transport.MaxConnsPerHost, s3MaxConnections)
	}
	if transport.ResponseHeaderTimeout != s3ResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, s3ResponseHeaderTimeout)
	}
}

func TestSanitizeS3TransportErrorRemovesPresignedURL(t *testing.T) {
	raw := &url.Error{
		Op:  "Put",
		URL: "https://bucket.s3.amazonaws.com/key?X-Amz-Signature=secret",
		Err: errors.New("net/http: timeout awaiting response headers"),
	}
	message := sanitizeS3TransportError(raw).Error()
	if strings.Contains(message, "X-Amz-") || strings.Contains(message, "secret") {
		t.Fatalf("sanitized error leaked presigned URL: %s", message)
	}
	if !strings.Contains(message, "timeout awaiting response headers") {
		t.Fatalf("sanitized error lost root cause: %s", message)
	}
}

func TestUploadToS3ReturnsTypedHTTPError(t *testing.T) {
	message := strings.Repeat("detailed-error-", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, message)
	}))
	defer server.Close()

	client := NewClient("")
	err := client.UploadToS3(context.Background(), server.URL, strings.NewReader("x"), 1, "", nil, nil)
	var uploadErr *S3UploadError
	if !errors.As(err, &uploadErr) || uploadErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v, want typed 503", err)
	}
	if uploadErr.Body != message {
		t.Fatalf("error body was truncated: got %d bytes, want %d", len(uploadErr.Body), len(message))
	}
}

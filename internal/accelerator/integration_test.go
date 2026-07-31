package accelerator

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mnemoo/cli/internal/upload"
)

func TestAcceleratorStagesEncryptedStreamAndUploadsRemotely(t *testing.T) {
	password := "correct horse battery staple"
	server, relayURL, closeRelay := newTestRelay(t, password)
	defer closeRelay()

	var received bytes.Buffer
	remote := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Errorf("remote method = %s", request.Method)
		}
		_, _ = io.Copy(&received, request.Body)
		response.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	server.httpClient = remote.Client()

	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.Fingerprint() == "" {
		t.Fatal("automatic certificate fingerprint is empty")
	}

	payload := bytes.Repeat([]byte("stake-stream-"), 300_000)
	var readBytes int64
	bodySent := false
	if err := client.Upload(ctx, bytes.NewReader(payload), int64(len(payload)), "application/octet-stream", remote.URL+"/object", func(delta int64) {
		readBytes += delta
	}, func() {
		bodySent = true
	}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !bodySent || readBytes != int64(len(payload)) {
		t.Fatalf("stream callbacks bodySent=%v bytes=%d", bodySent, readBytes)
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatal("remote server did not receive the original plaintext stream")
	}
	entries, err := os.ReadDir(server.config.SpoolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("encrypted spool was not cleaned: %v", entries)
	}
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskDone {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestAcceleratorRejectsWrongPasswordAndChangedPin(t *testing.T) {
	_, relayURL, closeRelay := newTestRelay(t, "correct horse battery staple")
	defer closeRelay()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wrongPassword, err := NewClient(relayURL, "this password is definitely wrong", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongPassword.Connect(ctx); err == nil {
		t.Fatal("wrong password was accepted")
	}

	wrongPin, err := NewClient(relayURL, "correct horse battery staple", "DEADBEEF")
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongPin.Connect(ctx); err == nil {
		t.Fatal("changed certificate pin was accepted")
	}
}

func TestAutomaticPinRejectsTLSServerWithoutPasswordProof(t *testing.T) {
	impostor := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"ok":true,"protocol":2}`)
	}))
	defer impostor.Close()

	client, err := NewClient(impostor.URL, "correct horse battery staple", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err == nil {
		t.Fatal("automatic pin accepted a TLS server without password proof")
	} else if !strings.Contains(err.Error(), "server proof") {
		t.Fatalf("automatic pin failed for the wrong reason: %v", err)
	}
}

func TestInterruptedStagingResumesFromAcceptedCiphertextOffset(t *testing.T) {
	password := "correct horse battery staple"
	server, relayURL, closeRelay := newTestRelay(t, password)
	defer closeRelay()

	var received bytes.Buffer
	remote := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(&received, request.Body)
		response.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	server.httpClient = remote.Client()

	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	taskID := "resumable-task-abcdefghijkl"
	payload := bytes.Repeat([]byte("unstable-network-"), 200_000)
	metadata, err := json.Marshal(taskMetadata{
		URL: remote.URL + "/object", ContentType: "application/octet-stream", Size: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := writeEncryptedPayload(&encrypted, client.authKey, taskID, metadata, bytes.NewReader(payload), nil); err != nil {
		t.Fatal(err)
	}
	all := encrypted.Bytes()
	split := len(all) / 3

	first := postEncryptedSegment(t, ctx, client, taskID, 0, int64(len(all)), all[:split])
	if first.Status != TaskReceiving || first.Received != int64(split) {
		t.Fatalf("first segment task = %+v", first)
	}
	second := postEncryptedSegment(t, ctx, client, taskID, int64(split), int64(len(all)), all[split:])
	if second.Status != TaskQueued {
		t.Fatalf("second segment task = %+v", second)
	}
	if err := client.waitTask(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatal("resumed upload did not reach the remote target intact")
	}
	if _, err := os.Stat(server.partialTaskPath(taskID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial spool still exists: %v", err)
	}
}

func TestEncryptedSpeedTest(t *testing.T) {
	_, relayURL, closeRelay := newTestRelay(t, "correct horse battery staple")
	defer closeRelay()
	client, err := NewClient(relayURL, "correct horse battery staple", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := client.SpeedTest(ctx, 8*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 8*1024*1024 || result.Connections != 1 ||
		result.ClientDuration <= 0 || result.ServerDuration <= 0 {
		t.Fatalf("speedtest result = %+v", result)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() ||
		transport.MaxConnsPerHost != relayParallelConnections {
		t.Fatalf("relay transport is not pinned to %d physical HTTP/1.1 lanes: %+v", relayParallelConnections, transport)
	}
}

func TestSpeedTestUsesFourPhysicalRelayConnections(t *testing.T) {
	password := "correct horse battery staple"
	arrived := make(chan string, relayParallelConnections)
	release := make(chan struct{})
	_, relayURL, closeRelay := newTestRelayWithHandler(t, password, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/v1/speedtest" {
				arrived <- request.RemoteAddr
				<-release
			}
			next.ServeHTTP(response, request)
		})
	})
	defer closeRelay()

	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		result SpeedTestResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, speedErr := client.SpeedTest(ctx, int64(relayParallelConnections)*upload.PartSize)
		done <- outcome{result: result, err: speedErr}
	}()

	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	connections := make(map[string]struct{}, relayParallelConnections)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(connections) < relayParallelConnections {
		select {
		case address := <-arrived:
			connections[address] = struct{}{}
		case early := <-done:
			t.Fatalf("speedtest ended before all physical lanes arrived: result=%+v err=%v", early.result, early.err)
		case <-timer.C:
			t.Fatalf("speedtest opened %d physical connections, want %d", len(connections), relayParallelConnections)
		}
	}
	close(release)
	released = true
	completed := <-done
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	if completed.result.Connections != relayParallelConnections {
		t.Fatalf("speedtest result = %+v", completed.result)
	}
}

func TestSpeedTestRetriesTransientLaneEOF(t *testing.T) {
	password := "correct horse battery staple"
	var requests atomic.Int32
	_, relayURL, closeRelay := newTestRelayWithHandler(t, password, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/v1/speedtest" && requests.Add(1) == 1 {
				hijacker, ok := response.(http.Hijacker)
				if !ok {
					t.Error("test relay does not support HTTP/1.1 hijacking")
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					t.Errorf("hijacking transient speedtest connection: %v", err)
					return
				}
				_ = connection.Close()
				return
			}
			next.ServeHTTP(response, request)
		})
	})
	defer closeRelay()

	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := client.SpeedTest(ctx, upload.PartSize)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || result.Bytes != upload.PartSize || result.Connections != 1 {
		t.Fatalf("transient EOF retry result=%+v requests=%d", result, requests.Load())
	}
}

func TestBackgroundDeliveryRetriesTransientRemoteFailure(t *testing.T) {
	password := "correct horse battery staple"
	server, relayURL, closeRelay := newTestRelay(t, password)
	defer closeRelay()
	var attempts atomic.Int32
	remote := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		if attempts.Add(1) < 3 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	server.httpClient = remote.Client()
	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	payload := bytes.NewReader(bytes.Repeat([]byte("retry-me-"), 10_000))
	if err := client.Upload(ctx, payload, int64(payload.Len()), "application/octet-stream", remote.URL, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("remote attempts = %d, want 3", got)
	}
}

func TestInactivePartialSessionExpiresAfterOneHour(t *testing.T) {
	server, _, closeRelay := newTestRelay(t, "correct horse battery staple")
	defer closeRelay()
	taskID := "expired-partial-abcdefghijkl"
	path := server.partialTaskPath(taskID)
	if err := os.WriteFile(path, []byte("ciphertext prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	server.tasks[taskID] = &Task{
		ID: taskID, Status: TaskReceiving, Received: 17,
		CreatedAt: old, UpdatedAt: old,
	}
	server.cleanupExpired(time.Now())
	if _, ok := server.getTask(taskID); ok {
		t.Fatal("expired partial task still exists")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired partial spool still exists: %v", err)
	}
}

func TestCompletePartialSessionRecoversAfterDaemonRestart(t *testing.T) {
	password := "correct horse battery staple"
	server, relayURL, closeRelay := newTestRelay(t, password)
	defer closeRelay()
	var received bytes.Buffer
	remote := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(&received, request.Body)
		response.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	server.httpClient = remote.Client()

	client, err := NewClient(relayURL, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	taskID := "restart-recovery-abcdefghijk"
	payload := bytes.Repeat([]byte("restart-safe-"), 100_000)
	metadata, err := json.Marshal(taskMetadata{
		URL: remote.URL + "/object", ContentType: "application/octet-stream", Size: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(server.partialTaskPath(taskID), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEncryptedPayload(file, client.authKey, taskID, metadata, bytes.NewReader(payload), nil); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.recoverTasks(); err != nil {
		t.Fatal(err)
	}
	if err := client.waitTask(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatal("recovered task did not reach remote target")
	}
}

func postEncryptedSegment(t *testing.T, ctx context.Context, client *Client, taskID string, offset, total int64, data []byte) Task {
	t.Helper()
	request, err := client.newRequest(ctx, http.MethodPost, "/v1/tasks/"+taskID+"/payload", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = int64(len(data))
	request.Header.Set(headerOffset, strconv.FormatInt(offset, 10))
	request.Header.Set(headerTotal, strconv.FormatInt(total, 10))
	response, err := client.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("segment status = %s", response.Status)
	}
	var task Task
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

func newTestRelay(t *testing.T, password string) (*Server, string, func()) {
	t.Helper()
	return newTestRelayWithHandler(t, password, func(handler http.Handler) http.Handler { return handler })
}

func newTestRelayWithHandler(t *testing.T, password string, wrap func(http.Handler) http.Handler) (*Server, string, func()) {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "identity.crt")
	keyPath := filepath.Join(dir, "identity.key")
	_, fingerprintBytes, err := generateIdentity(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	authKey, err := DeriveAuthKey(password, fingerprintBytes)
	if err != nil {
		t.Fatal(err)
	}
	config := DaemonConfig{
		Listen:              ":0",
		CertFile:            certPath,
		KeyFile:             keyPath,
		SpoolDir:            filepath.Join(dir, "spool"),
		AuthKey:             base64.RawStdEncoding.EncodeToString(authKey),
		MaxTaskBytes:        64 * 1024 * 1024,
		AllowPrivateTargets: true,
	}
	server, err := NewServer(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewUnstartedServer(wrap(server))
	relay.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
	}
	relay.StartTLS()
	return server, relay.URL, relay.Close
}

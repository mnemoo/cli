package accelerator

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mnemoo/cli/internal/upload"
)

type Client struct {
	endpoint    *url.URL
	address     string
	password    string
	pinned      string
	fingerprint string
	authKey     []byte
	httpClient  *http.Client
	connectOnce sync.Once
	connectErr  error
}

func NewClient(address, password, pinnedFingerprint string) (*Client, error) {
	endpoint, err := normalizeAddress(address)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("accelerator password is required")
	}
	return &Client{
		endpoint: endpoint,
		address:  address,
		password: password,
		pinned:   normalizeFingerprint(pinnedFingerprint),
	}, nil
}

// ConnectConfigured resolves a profile or raw IP address, performs the
// password-authenticated first connection, and persists the certificate pin
// only after authentication succeeds.
func ConnectConfigured(ctx context.Context, reference, password string) (*Client, string, error) {
	name, address, pinned, err := ResolveProfile(reference)
	if err != nil {
		return nil, "", err
	}
	client, err := NewClient(address, password, pinned)
	if err != nil {
		return nil, "", err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, "", err
	}
	if pinned == "" {
		if name == "" {
			name = AutoProfileName(address)
		}
		if err := SaveProfile(name, Profile{
			Address: address, Fingerprint: client.Fingerprint(),
		}, false); err != nil {
			return nil, "", err
		}
	}
	return client, name, nil
}

func (c *Client) Connect(ctx context.Context) error {
	c.connectOnce.Do(func() { c.connectErr = c.connect(ctx) })
	return c.connectErr
}

func (c *Client) Fingerprint() string {
	return c.fingerprint
}

func (c *Client) connect(ctx context.Context) error {
	host := c.endpoint.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, // verified by exact certificate fingerprint below
		},
	}
	connection, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf("connecting to accelerator: %w", err)
	}
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		_ = connection.Close()
		return errors.New("accelerator connection did not negotiate TLS")
	}
	state := tlsConnection.ConnectionState()
	_ = tlsConnection.Close()
	if len(state.PeerCertificates) != 1 {
		return errors.New("accelerator must present exactly one pinned certificate")
	}
	rawCertificate := state.PeerCertificates[0].Raw
	fingerprint := Fingerprint(rawCertificate)
	if c.pinned != "" && normalizeFingerprint(fingerprint) != c.pinned {
		return fmt.Errorf("accelerator identity changed: pinned %s, received %s", c.pinned, fingerprint)
	}
	sum := sha256Bytes(rawCertificate)
	authKey, err := DeriveAuthKey(c.password, sum)
	if err != nil {
		return err
	}
	c.authKey = authKey
	c.fingerprint = fingerprint
	pinned := normalizeFingerprint(fingerprint)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = relayParallelConnections
	transport.MaxIdleConnsPerHost = relayParallelConnections
	transport.MaxConnsPerHost = relayParallelConnections
	// A relay is valuable on paths where one TCP flow cannot fill the link.
	// HTTP/2 would multiplex every concurrent part back onto one TCP flow, so
	// explicitly use HTTP/1.1 and let the transport keep four physical lanes.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // VerifyConnection enforces the TOFU pin
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("accelerator certificate chain is not pinned")
			}
			if normalizeFingerprint(Fingerprint(state.PeerCertificates[0].Raw)) != pinned {
				return errors.New("accelerator certificate pin mismatch")
			}
			return nil
		},
	}
	c.httpClient = &http.Client{
		Transport: transport,
		Timeout:   0,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("accelerator redirects are disabled")
		},
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := c.newRequest(probeCtx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return err
	}
	probeNonce := request.Header.Get(headerNonce)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("authenticating accelerator: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return errors.New("accelerator rejected the password")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("accelerator health returned %s", response.Status)
	}
	var health struct {
		OK       bool   `json:"ok"`
		Protocol int    `json:"protocol"`
		Proof    string `json:"proof"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&health); err != nil {
		return fmt.Errorf("reading accelerator health: %w", err)
	}
	if !health.OK || health.Protocol != protocolVersion {
		return fmt.Errorf("accelerator protocol mismatch: server=%d client=%d", health.Protocol, protocolVersion)
	}
	if !validServerProof(c.authKey, probeNonce, health.Proof) {
		return errors.New("accelerator failed password-authenticated server proof")
	}
	return nil
}

func (c *Client) Upload(ctx context.Context, body io.Reader, size int64, contentType, remoteURL string, onRead func(int64), onBodySent func()) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}
	seeker, resumable := body.(io.Seeker)
	taskID, err := randomToken(18)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(taskMetadata{URL: remoteURL, ContentType: contentType, Size: size})
	if err != nil {
		return err
	}
	total := encryptedStreamSize(int64(len(metadata)), size)
	path := "/v1/tasks/" + taskID + "/payload"
	offset := int64(0)
	lastProgress := time.Now()
	reportedPlain := int64(0)
	retryDelay := 250 * time.Millisecond

	for {
		if time.Since(lastProgress) >= incompleteRetention {
			return errors.New("accelerator resume session expired after one hour without progress")
		}
		if resumable {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("rewinding upload for accelerator resume: %w", err)
			}
		} else if offset != 0 {
			return errors.New("accelerator staging was interrupted and the source stream is not seekable")
		}

		attemptRead := int64(0)
		reader, writer := io.Pipe()
		encryptErr := make(chan error, 1)
		go func() {
			encryptorErr := writeEncryptedPayload(writer, c.authKey, taskID, metadata, body, func(delta int64) {
				attemptRead += delta
				if attemptRead > reportedPlain {
					fresh := attemptRead - reportedPlain
					reportedPlain = attemptRead
					if onRead != nil {
						onRead(fresh)
					}
				}
			})
			encryptErr <- encryptorErr
			_ = writer.CloseWithError(encryptorErr)
		}()
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
				_ = reader.CloseWithError(err)
				<-encryptErr
				return fmt.Errorf("rebuilding encrypted resume prefix: %w", err)
			}
		}

		request, err := c.newRequest(ctx, http.MethodPost, path, reader)
		if err != nil {
			_ = reader.CloseWithError(err)
			<-encryptErr
			return err
		}
		request.ContentLength = total - offset
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set(headerOffset, strconv.FormatInt(offset, 10))
		request.Header.Set(headerTotal, strconv.FormatInt(total, 10))
		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			_ = reader.CloseWithError(requestErr)
			encryptorErr := <-encryptErr
			if encryptorErr != nil && !errors.Is(encryptorErr, io.ErrClosedPipe) {
				return fmt.Errorf("encrypting accelerator task: %w", encryptorErr)
			}
			if !resumable {
				return fmt.Errorf("staging encrypted accelerator task: %w", requestErr)
			}
			if err := waitRetry(ctx, retryDelay); err != nil {
				return err
			}
			retryDelay = min(retryDelay*2, 5*time.Second)
			continue
		}
		encryptorErr := <-encryptErr
		var task Task
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&task)
		_ = response.Body.Close()
		if encryptorErr != nil && !errors.Is(encryptorErr, io.ErrClosedPipe) {
			return fmt.Errorf("encrypting accelerator task: %w", encryptorErr)
		}
		if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusConflict {
			return fmt.Errorf("accelerator rejected task: %s", response.Status)
		}
		if decodeErr != nil {
			return fmt.Errorf("reading accelerator resume state: %w", decodeErr)
		}
		if task.Received > offset {
			lastProgress = time.Now()
			offset = task.Received
			retryDelay = 250 * time.Millisecond
		}
		switch task.Status {
		case TaskQueued, TaskUploading, TaskDone, TaskFailed, TaskCancelled:
			if onBodySent != nil {
				onBodySent()
			}
			return c.waitTask(ctx, taskID)
		case TaskReceiving:
			if !resumable {
				return errors.New("accelerator accepted only part of a non-seekable source stream")
			}
			if offset >= total {
				return c.waitTask(ctx, taskID)
			}
		default:
			return fmt.Errorf("accelerator returned unknown task status %q", task.Status)
		}
	}
}

func (c *Client) waitTask(ctx context.Context, taskID string) error {
	lastResponse := time.Now()
	retryDelay := 500 * time.Millisecond
	for {
		task, err := c.GetTask(ctx, taskID)
		if err != nil {
			if time.Since(lastResponse) >= incompleteRetention {
				return fmt.Errorf("accelerator status was unreachable for one hour: %w", err)
			}
			if err := waitRetry(ctx, retryDelay); err != nil {
				return err
			}
			retryDelay = min(retryDelay*2, 5*time.Second)
			continue
		}
		lastResponse = time.Now()
		retryDelay = 500 * time.Millisecond
		switch task.Status {
		case TaskDone:
			return nil
		case TaskFailed:
			if task.Error == "" {
				return errors.New("accelerator task failed")
			}
			return errors.New(task.Error)
		case TaskCancelled:
			return context.Canceled
		}
		if err := waitRetry(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/tasks", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing accelerator tasks: %s", response.Status)
	}
	var result tasksResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&result); err != nil {
		return nil, err
	}
	return result.Tasks, nil
}

func (c *Client) GetTask(ctx context.Context, taskID string) (Task, error) {
	if err := c.Connect(ctx); err != nil {
		return Task{}, err
	}
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil)
	if err != nil {
		return Task{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Task{}, fmt.Errorf("reading accelerator task: %s", response.Status)
	}
	var task Task
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (c *Client) SpeedTest(ctx context.Context, size int64) (SpeedTestResult, error) {
	if size <= 0 || size > maxSpeedTestBytes {
		return SpeedTestResult{}, fmt.Errorf("speedtest size must be between 1 byte and %d bytes", maxSpeedTestBytes)
	}
	if err := c.Connect(ctx); err != nil {
		return SpeedTestResult{}, err
	}
	chunkCount := int((size + upload.PartSize - 1) / upload.PartSize)
	connections := min(relayParallelConnections, chunkCount)
	type lane struct {
		result speedTestLaneResult
		err    error
	}
	laneSizes := make(chan int64, chunkCount)
	laneResults := make(chan lane, chunkCount)
	laneCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	started := time.Now()
	remaining := size
	for remaining > 0 {
		laneSize := min(remaining, upload.PartSize)
		laneSizes <- laneSize
		remaining -= laneSize
	}
	close(laneSizes)
	for range connections {
		go func() {
			for laneSize := range laneSizes {
				if err := laneCtx.Err(); err != nil {
					laneResults <- lane{err: err}
					continue
				}
				result, err := c.speedTestLaneWithRetry(laneCtx, laneSize)
				laneResults <- lane{result: result, err: err}
			}
		}()
	}

	result := SpeedTestResult{Connections: connections}
	var firstServerStart time.Time
	var lastServerFinish time.Time
	var longestServerDuration time.Duration
	completeServerWindow := true
	var firstErr error
	for range chunkCount {
		laneResult := <-laneResults
		if laneResult.err != nil {
			if firstErr == nil {
				firstErr = laneResult.err
				cancel()
			}
			continue
		}
		result.Bytes += laneResult.result.Bytes
		if laneResult.result.ServerDuration > longestServerDuration {
			longestServerDuration = laneResult.result.ServerDuration
		}
		if laneResult.result.ServerStartedAt.IsZero() || laneResult.result.ServerFinishedAt.IsZero() {
			completeServerWindow = false
		}
		if firstServerStart.IsZero() || laneResult.result.ServerStartedAt.Before(firstServerStart) {
			firstServerStart = laneResult.result.ServerStartedAt
		}
		if lastServerFinish.IsZero() || laneResult.result.ServerFinishedAt.After(lastServerFinish) {
			lastServerFinish = laneResult.result.ServerFinishedAt
		}
	}
	result.ClientDuration = time.Since(started)
	if firstErr != nil {
		return SpeedTestResult{}, firstErr
	}
	if completeServerWindow && !firstServerStart.IsZero() && lastServerFinish.After(firstServerStart) {
		result.ServerDuration = lastServerFinish.Sub(firstServerStart)
	} else if chunkCount == 1 && longestServerDuration > 0 {
		// Protocol-v2 daemons built before parallel speed tests return only a
		// duration. It remains exact when the test needs one 8 MiB request.
		result.ServerDuration = longestServerDuration
	} else {
		// An older daemon has no shared server timestamps. Client wall time is
		// the conservative aggregate receive window; using one chunk duration
		// here would overstate throughput by the number of chunks.
		result.ServerDuration = result.ClientDuration
	}
	if result.Bytes != size {
		return SpeedTestResult{}, fmt.Errorf("accelerator speedtest received %d of %d bytes", result.Bytes, size)
	}
	return result, nil
}

func (c *Client) speedTestLaneWithRetry(ctx context.Context, size int64) (speedTestLaneResult, error) {
	const attempts = 3
	delay := 100 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := c.speedTestLane(ctx, size)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == attempts || !retryableSpeedTestError(err) {
			break
		}
		if err := waitRetry(ctx, delay); err != nil {
			return speedTestLaneResult{}, err
		}
		delay *= 2
	}
	return speedTestLaneResult{}, fmt.Errorf("speedtest lane failed after retry: %w", lastErr)
}

func retryableSpeedTestError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func (c *Client) speedTestLane(ctx context.Context, size int64) (speedTestLaneResult, error) {
	taskID, err := randomToken(18)
	if err != nil {
		return speedTestLaneResult{}, err
	}
	reader, writer := io.Pipe()
	encryptErr := make(chan error, 1)
	go func() {
		err := writeEncryptedPayload(
			writer,
			c.authKey,
			taskID,
			speedTestMetadata,
			io.LimitReader(zeroReader{}, size),
			nil,
		)
		encryptErr <- err
		_ = writer.CloseWithError(err)
	}()
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/speedtest", reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-encryptErr
		return speedTestLaneResult{}, err
	}
	request.ContentLength = encryptedStreamSize(int64(len(speedTestMetadata)), size)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(headerTaskID, taskID)
	request.Header.Set(headerPlainSize, strconv.FormatInt(size, 10))
	response, err := c.httpClient.Do(request)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-encryptErr
		return speedTestLaneResult{}, fmt.Errorf("running accelerator speedtest lane: %w", err)
	}
	encryptorErr := <-encryptErr
	defer func() { _ = response.Body.Close() }()
	if encryptorErr != nil {
		return speedTestLaneResult{}, encryptorErr
	}
	if response.StatusCode != http.StatusOK {
		return speedTestLaneResult{}, fmt.Errorf("accelerator speedtest lane returned %s", response.Status)
	}
	var result speedTestLaneResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result); err != nil {
		return speedTestLaneResult{}, err
	}
	if result.Bytes != size {
		return speedTestLaneResult{}, fmt.Errorf("accelerator speedtest lane received %d of %d bytes", result.Bytes, size)
	}
	return result, nil
}

type zeroReader struct{}

func (zeroReader) Read(output []byte) (int, error) {
	clear(output)
	return len(output), nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	target := c.endpoint.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := randomToken(18)
	if err != nil {
		return nil, err
	}
	request.Header.Set(headerTimestamp, timestamp)
	request.Header.Set(headerNonce, nonce)
	request.Header.Set(headerAuth, requestMAC(c.authKey, method, request.URL.EscapedPath(), timestamp, nonce))
	return request, nil
}

func normalizeAddress(address string) (*url.URL, error) {
	value := strings.TrimSpace(address)
	if value == "" {
		return nil, errors.New("accelerator address is required")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parsing accelerator address: %w", err)
	}
	if endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil {
		return nil, errors.New("accelerator address must be an HTTPS host or IP")
	}
	if endpoint.Port() == "" {
		endpoint.Host = net.JoinHostPort(endpoint.Hostname(), "7775")
	}
	endpoint.Path = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint, nil
}

func normalizeFingerprint(value string) string {
	value = strings.TrimSpace(strings.ToUpper(value))
	value = strings.TrimPrefix(value, "SHA256:")
	value = strings.ReplaceAll(value, ":", "")
	return value
}

func sha256Bytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

// UploadClient preserves the entire Stake upload contract and only replaces
// the presigned S3 PUT transport. Planning, ETag comparison, server-side copy,
// delete, multipart initialization/completion, and publish remain in stakecli.
type UploadClient struct {
	upload.Client
	Accelerator    *Client
	FallbackDirect bool
	direct         atomic.Bool
}

func (c *UploadClient) UploadToS3(ctx context.Context, remoteURL string, body io.Reader, size int64, contentType string, onRead func(int64), onBodySent func()) error {
	if c == nil || c.Accelerator == nil {
		return errors.New("accelerator transport is not configured")
	}
	if c.direct.Load() {
		return c.Client.UploadToS3(ctx, remoteURL, body, size, contentType, onRead, onBodySent)
	}
	err := c.Accelerator.Upload(ctx, body, size, contentType, remoteURL, onRead, onBodySent)
	if err != nil && c.FallbackDirect && !expiredRemoteURL(err) {
		// The current source stream may already have been consumed while it was
		// staged. Switch the following retry to direct transport; retryPut will
		// construct a fresh SectionReader for exactly the same file part.
		c.direct.Store(true)
	}
	return err
}

var _ upload.Client = (*UploadClient)(nil)

func expiredRemoteURL(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "ExpiredToken") ||
		strings.Contains(message, "token has expired") ||
		strings.Contains(message, "Request has expired")
}

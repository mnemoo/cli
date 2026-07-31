package accelerator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	headerAuth           = "X-Stake-Accelerator-Auth"
	headerTimestamp      = "X-Stake-Accelerator-Timestamp"
	headerNonce          = "X-Stake-Accelerator-Nonce"
	headerOffset         = "X-Stake-Accelerator-Offset"
	headerTotal          = "X-Stake-Accelerator-Total"
	headerTaskID         = "X-Stake-Accelerator-Task"
	headerPlainSize      = "X-Stake-Accelerator-Plain-Size"
	maxErrorBody         = 64 * 1024
	maxSpeedTestBytes    = int64(1024 * 1024 * 1024)
	incompleteRetention  = time.Hour
	remoteDeliveryWindow = time.Hour
)

type Server struct {
	config     DaemonConfig
	authKey    []byte
	httpClient *http.Client
	logger     *log.Logger

	mu       sync.Mutex
	tasks    map[string]*Task
	cancels  map[string]context.CancelFunc
	receives map[string]bool
	nonces   map[string]time.Time
	workers  chan struct{}
}

func NewServer(config DaemonConfig, logger *log.Logger) (*Server, error) {
	authKey, err := base64.RawStdEncoding.DecodeString(config.AuthKey)
	if err != nil || len(authKey) != 32 {
		return nil, errors.New("accelerator auth key must contain 32 bytes")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if err := os.MkdirAll(config.SpoolDir, 0700); err != nil {
		return nil, fmt.Errorf("creating accelerator spool: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 32
	transport.MaxConnsPerHost = 32
	if !config.AllowPrivateTargets {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid accelerator target address: %w", err)
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolving accelerator target failed: %w", err)
			}
			var lastErr error
			for _, resolved := range addresses {
				if !publicIP(resolved.IP) {
					continue
				}
				conn, dialErr := dialer.DialContext(
					ctx,
					network,
					net.JoinHostPort(resolved.IP.String(), port),
				)
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errors.New("accelerator target resolved only to private or local addresses")
		}
	}
	return &Server{
		config:  config,
		authKey: authKey,
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("redirects are disabled for accelerator targets")
			},
		},
		logger:   logger,
		tasks:    make(map[string]*Task),
		cancels:  make(map[string]context.CancelFunc),
		receives: make(map[string]bool),
		nonces:   make(map[string]time.Time),
		workers:  make(chan struct{}, 16),
	}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	if err := s.recoverTasks(); err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              s.config.Listen,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	go s.cleanupLoop(ctx)
	s.logger.Printf("stake accelerator listening on %s", s.config.Listen)
	err := httpServer.ListenAndServeTLS(s.config.CertFile, s.config.KeyFile)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !s.authenticate(request) {
		response.Header().Set("Connection", "close")
		response.WriteHeader(http.StatusNotFound)
		return
	}
	path := request.URL.Path
	switch {
	case request.Method == http.MethodGet && path == "/v1/health":
		writeJSON(response, http.StatusOK, map[string]any{
			"ok":       true,
			"protocol": protocolVersion,
			"proof":    serverProof(s.authKey, request.Header.Get(headerNonce)),
		})
	case request.Method == http.MethodGet && path == "/v1/tasks":
		writeJSON(response, http.StatusOK, tasksResponse{Protocol: protocolVersion, Tasks: s.listTasks()})
	case request.Method == http.MethodGet && path == "/v1/events":
		s.watchTasks(response, request)
	case request.Method == http.MethodPost && path == "/v1/speedtest":
		s.speedTest(response, request)
	case strings.HasPrefix(path, "/v1/tasks/"):
		s.handleTask(response, request)
	default:
		response.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) authenticate(request *http.Request) bool {
	timestamp := request.Header.Get(headerTimestamp)
	nonce := request.Header.Get(headerNonce)
	supplied := request.Header.Get(headerAuth)
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(nonce) < 16 || len(nonce) > 128 || len(supplied) < 32 {
		return false
	}
	now := time.Now()
	if delta := now.Sub(time.Unix(unix, 0)); delta < -time.Minute || delta > time.Minute {
		return false
	}
	if !validRequestMAC(s.authKey, request.Method, request.URL.EscapedPath(), timestamp, nonce, supplied) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for value, expiry := range s.nonces {
		if now.After(expiry) {
			delete(s.nonces, value)
		}
	}
	if _, replayed := s.nonces[nonce]; replayed {
		return false
	}
	s.nonces[nonce] = now.Add(2 * time.Minute)
	return true
}

func (s *Server) handleTask(response http.ResponseWriter, request *http.Request) {
	rest := strings.TrimPrefix(request.URL.Path, "/v1/tasks/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || !validTaskID(parts[0]) {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	taskID := parts[0]
	if request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "payload" {
		s.receiveTask(response, request, taskID)
		return
	}
	if len(parts) != 1 {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	switch request.Method {
	case http.MethodGet:
		task, ok := s.getTask(taskID)
		if !ok {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(response, http.StatusOK, task)
	case http.MethodDelete:
		if !s.cancelTask(taskID) {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) receiveTask(response http.ResponseWriter, request *http.Request, taskID string) {
	maxEncrypted := s.config.MaxTaskBytes + s.config.MaxTaskBytes/streamPlainChunkSize*32 + 128*1024
	offset, offsetErr := strconv.ParseInt(request.Header.Get(headerOffset), 10, 64)
	total, totalErr := strconv.ParseInt(request.Header.Get(headerTotal), 10, 64)
	if offsetErr != nil || totalErr != nil || offset < 0 || total <= 0 || total > maxEncrypted ||
		request.ContentLength <= 0 || offset+request.ContentLength > total {
		response.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	partialPath := s.partialTaskPath(taskID)
	info, err := os.Stat(partialPath)
	currentSize := int64(0)
	if err == nil {
		currentSize = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	existing, exists := s.tasks[taskID]
	if exists && existing.Status != TaskReceiving {
		task := *existing
		s.mu.Unlock()
		writeJSON(response, http.StatusAccepted, task)
		return
	}
	if s.receives[taskID] {
		task := Task{ID: taskID, Status: TaskReceiving, Received: currentSize, Expected: total}
		if existing != nil {
			task = *existing
		}
		s.mu.Unlock()
		writeJSON(response, http.StatusConflict, task)
		return
	}
	if offset != currentSize || (exists && existing.Expected != 0 && existing.Expected != total) {
		task := Task{ID: taskID, Status: TaskReceiving, Received: currentSize, Expected: total}
		if existing != nil {
			task = *existing
			task.Received = currentSize
		}
		s.mu.Unlock()
		writeJSON(response, http.StatusConflict, task)
		return
	}
	now := time.Now().UTC()
	if !exists {
		existing = &Task{
			ID: taskID, Status: TaskReceiving, Received: currentSize, Expected: total,
			CreatedAt: now, UpdatedAt: now,
		}
		s.tasks[taskID] = existing
	} else {
		existing.Expected = total
		existing.Received = currentSize
		existing.UpdatedAt = now
	}
	s.receives[taskID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.receives, taskID)
		s.mu.Unlock()
	}()

	temp, err := os.OpenFile(partialPath, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		s.failTask(taskID, "cannot create encrypted spool")
		response.WriteHeader(http.StatusInsufficientStorage)
		return
	}
	if _, err := temp.Seek(offset, io.SeekStart); err != nil {
		_ = temp.Close()
		s.failTask(taskID, "cannot seek encrypted spool")
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	progress := &receiveWriter{
		writer: temp,
		onWrite: func(written int64) {
			s.updateTask(taskID, func(task *Task) {
				task.Status = TaskReceiving
				task.Received = offset + written
				task.Expected = total
				task.Error = ""
			})
		},
	}
	written, copyErr := io.CopyBuffer(progress, io.LimitReader(request.Body, request.ContentLength+1), make([]byte, streamPlainChunkSize))
	if copyErr == nil && written != request.ContentLength {
		copyErr = io.ErrUnexpectedEOF
	}
	if copyErr == nil && offset+written == total && s.config.SyncSpool {
		copyErr = temp.Sync()
	}
	closeErr := temp.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	received := offset + written
	s.updateTask(taskID, func(task *Task) {
		task.Status = TaskReceiving
		task.Received = received
		task.Expected = total
		task.Error = ""
	})
	if copyErr != nil {
		// Keep the authenticated ciphertext prefix. The client can reconnect
		// and continue exactly at Received until the one-hour inactivity TTL.
		if request.Context().Err() == nil {
			s.failTask(taskID, "cannot write encrypted spool")
			response.WriteHeader(http.StatusInsufficientStorage)
		}
		return
	}
	if received < total {
		task, _ := s.getTask(taskID)
		writeJSON(response, http.StatusAccepted, task)
		return
	}

	finalPath := s.taskPath(taskID)
	if err := os.Rename(partialPath, finalPath); err != nil {
		s.failTask(taskID, "cannot commit encrypted spool")
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	s.updateTask(taskID, func(task *Task) {
		task.Status = TaskQueued
		task.Received = written
		now := time.Now().UTC()
		task.StagedAt = &now
	})
	task, _ := s.getTask(taskID)
	writeJSON(response, http.StatusAccepted, task)
	go s.runTask(taskID)
}

type receiveWriter struct {
	writer  io.Writer
	written int64
	onWrite func(int64)
}

func (w *receiveWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if n > 0 && w.onWrite != nil {
		w.onWrite(w.written)
	}
	return n, err
}

func (s *Server) runTask(taskID string) {
	s.workers <- struct{}{}
	defer func() { <-s.workers }()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	task, exists := s.tasks[taskID]
	if !exists || task.Status == TaskCancelled {
		s.mu.Unlock()
		cancel()
		_ = os.Remove(s.taskPath(taskID))
		return
	}
	s.cancels[taskID] = cancel
	task.Status = TaskUploading
	task.UpdatedAt = time.Now().UTC()
	started := task.UpdatedAt
	task.StartedAt = &started
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels, taskID)
		s.mu.Unlock()
		_ = os.Remove(s.taskPath(taskID))
	}()

	err := s.uploadTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.updateTask(taskID, func(task *Task) {
				task.Status = TaskCancelled
				task.Error = ""
			})
			return
		}
		s.failTask(taskID, sanitizeTaskError(err))
		return
	}
	s.updateTask(taskID, func(task *Task) {
		task.Status = TaskDone
		task.Error = ""
		task.Uploaded = task.Size
		now := time.Now().UTC()
		task.DoneAt = &now
	})
}

func (s *Server) uploadTask(ctx context.Context, taskID string) error {
	var lastErr error
	deadline := time.Now().Add(remoteDeliveryWindow)
	delay := time.Duration(0)
	for {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		retry, err := s.uploadTaskOnce(ctx, taskID)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("remote delivery retry window expired: %w", lastErr)
		}
		if delay == 0 {
			delay = 500 * time.Millisecond
		} else {
			delay = min(delay*2, 30*time.Second)
		}
	}
}

func (s *Server) uploadTaskOnce(ctx context.Context, taskID string) (bool, error) {
	file, err := os.Open(s.taskPath(taskID))
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	decryptor, err := newDecryptReader(file, s.authKey, taskID)
	if err != nil {
		return false, err
	}
	metadataFrame, err := decryptor.nextFrame()
	if err != nil {
		return false, fmt.Errorf("reading encrypted task metadata: %w", err)
	}
	var metadata taskMetadata
	if err := json.Unmarshal(metadataFrame, &metadata); err != nil {
		return false, errors.New("encrypted task metadata is invalid")
	}
	if metadata.Size <= 0 || metadata.Size > s.config.MaxTaskBytes {
		return false, errors.New("encrypted task size is outside allowed limits")
	}
	if err := s.validateTarget(ctx, metadata.URL); err != nil {
		return false, err
	}
	s.updateTask(taskID, func(task *Task) { task.Size = metadata.Size })
	counted := &countingReader{
		reader: io.LimitReader(decryptor, metadata.Size),
		onRead: func(total int64) {
			s.updateTask(taskID, func(task *Task) { task.Uploaded = total })
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, metadata.URL, counted)
	if err != nil {
		return false, errors.New("creating remote PUT failed")
	}
	request.ContentLength = metadata.Size
	if metadata.ContentType != "" {
		request.Header.Set("Content-Type", metadata.ContentType)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return true, fmt.Errorf("remote PUT failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if counted.total != metadata.Size {
			return false, fmt.Errorf("remote PUT consumed %d of %d bytes", counted.total, metadata.Size)
		}
		return false, nil
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	err = fmt.Errorf("remote PUT returned %d: %s", response.StatusCode, message)
	return response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500, err
}

func (s *Server) validateTarget(ctx context.Context, rawURL string) error {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return errors.New("accelerator target must be a public HTTPS URL")
	}
	if s.config.AllowPrivateTargets {
		if target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
			return errors.New("accelerator test target URL is not allowed")
		}
		return nil
	}
	if target.Scheme != "https" {
		return errors.New("accelerator target must be a public HTTPS URL")
	}
	if target.User != nil {
		return errors.New("accelerator target URL is not allowed")
	}
	if target.Port() != "" && target.Port() != "443" {
		return errors.New("accelerator target URL is not allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, target.Hostname())
	if err != nil {
		return fmt.Errorf("resolving accelerator target failed: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("accelerator target did not resolve")
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return errors.New("accelerator target resolved to a private or local address")
		}
	}
	return nil
}

func (s *Server) speedTest(response http.ResponseWriter, request *http.Request) {
	taskID := request.Header.Get(headerTaskID)
	plainSize, err := strconv.ParseInt(request.Header.Get(headerPlainSize), 10, 64)
	if !validTaskID(taskID) || err != nil || plainSize <= 0 || plainSize > maxSpeedTestBytes {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	expected := encryptedStreamSize(int64(len(speedTestMetadata)), plainSize)
	if request.ContentLength != expected {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	started := time.Now()
	startedAt := started.UTC()
	decryptor, err := newDecryptReader(io.LimitReader(request.Body, expected), s.authKey, taskID)
	if err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	metadata, err := decryptor.nextFrame()
	if err != nil || string(metadata) != string(speedTestMetadata) {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	received, err := io.CopyBuffer(io.Discard, io.LimitReader(decryptor, plainSize), make([]byte, streamPlainChunkSize))
	if err != nil || received != plainSize {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	var trailing [1]byte
	if n, trailingErr := decryptor.Read(trailing[:]); n != 0 || !errors.Is(trailingErr, io.EOF) {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	finishedAt := time.Now().UTC()
	writeJSON(response, http.StatusOK, speedTestLaneResult{
		Bytes:            received,
		ServerDuration:   finishedAt.Sub(startedAt),
		ServerStartedAt:  startedAt,
		ServerFinishedAt: finishedAt,
	})
}

func publicIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func (s *Server) recoverTasks() error {
	var queued []string
	entries, err := filepath.Glob(filepath.Join(s.config.SpoolDir, "*.task"))
	if err != nil {
		return err
	}
	for _, path := range entries {
		taskID := strings.TrimSuffix(filepath.Base(path), ".task")
		if !validTaskID(taskID) {
			_ = os.Remove(path)
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		now := info.ModTime().UTC()
		s.tasks[taskID] = &Task{
			ID: taskID, Status: TaskQueued, Received: info.Size(),
			CreatedAt: now, UpdatedAt: now,
		}
		queued = append(queued, taskID)
	}
	partials, err := filepath.Glob(filepath.Join(s.config.SpoolDir, "*.partial"))
	if err != nil {
		return err
	}
	for _, path := range partials {
		taskID := strings.TrimSuffix(filepath.Base(path), ".partial")
		if !validTaskID(taskID) {
			_ = os.Remove(path)
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		now := info.ModTime().UTC()
		if time.Since(now) > incompleteRetention {
			_ = os.Remove(path)
			continue
		}
		if s.promoteCompletePartial(taskID, path, info.Size(), now) {
			queued = append(queued, taskID)
			continue
		}
		s.tasks[taskID] = &Task{
			ID: taskID, Status: TaskReceiving, Received: info.Size(),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	for _, taskID := range queued {
		go s.runTask(taskID)
	}
	return nil
}

func (s *Server) promoteCompletePartial(taskID, path string, size int64, timestamp time.Time) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	decryptor, err := newDecryptReader(file, s.authKey, taskID)
	if err != nil {
		_ = file.Close()
		return false
	}
	metadataFrame, err := decryptor.nextFrame()
	_ = file.Close()
	if err != nil {
		return false
	}
	var metadata taskMetadata
	if json.Unmarshal(metadataFrame, &metadata) != nil || metadata.Size <= 0 ||
		encryptedStreamSize(int64(len(metadataFrame)), metadata.Size) != size {
		return false
	}
	if os.Rename(path, s.taskPath(taskID)) != nil {
		return false
	}
	s.tasks[taskID] = &Task{
		ID: taskID, Status: TaskQueued, Received: size, Expected: size, Size: metadata.Size,
		CreatedAt: timestamp, UpdatedAt: timestamp, StagedAt: &timestamp,
	}
	return true
}

func (s *Server) taskPath(taskID string) string {
	return filepath.Join(s.config.SpoolDir, taskID+".task")
}

func (s *Server) partialTaskPath(taskID string) string {
	return filepath.Join(s.config.SpoolDir, taskID+".partial")
}

func (s *Server) getTask(taskID string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return Task{}, false
	}
	return *task, true
}

func (s *Server) listTasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		result = append(result, *task)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (s *Server) updateTask(taskID string, update func(*Task)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return
	}
	update(task)
	task.UpdatedAt = time.Now().UTC()
}

func (s *Server) failTask(taskID, message string) {
	s.updateTask(taskID, func(task *Task) {
		task.Status = TaskFailed
		task.Error = message
	})
}

func (s *Server) cancelTask(taskID string) bool {
	s.mu.Lock()
	task, ok := s.tasks[taskID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	task.Status = TaskCancelled
	task.Error = ""
	task.UpdatedAt = time.Now().UTC()
	cancel := s.cancels[taskID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = os.Remove(s.taskPath(taskID))
	_ = os.Remove(s.partialTaskPath(taskID))
	return true
}

func (s *Server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired(time.Now())
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) cleanupExpired(now time.Time) {
	cutoff := now.Add(-defaultRetention)
	var partials []string
	s.mu.Lock()
	for id, task := range s.tasks {
		expiredTerminal := (task.Status == TaskDone || task.Status == TaskFailed || task.Status == TaskCancelled) &&
			task.UpdatedAt.Before(cutoff)
		expiredPartial := task.Status == TaskReceiving && task.UpdatedAt.Before(cutoff) && !s.receives[id]
		if expiredTerminal || expiredPartial {
			delete(s.tasks, id)
			if expiredPartial {
				partials = append(partials, s.partialTaskPath(id))
			}
		}
	}
	s.mu.Unlock()
	for _, path := range partials {
		_ = os.Remove(path)
	}
}

func (s *Server) watchTasks(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		response.WriteHeader(http.StatusNotImplemented)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		data, err := json.Marshal(tasksResponse{Protocol: protocolVersion, Tasks: s.listTasks()})
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(response, "event: tasks\ndata: %s\n\n", data)
		flusher.Flush()
		select {
		case <-ticker.C:
		case <-request.Context().Done():
			return
		}
	}
}

func validTaskID(taskID string) bool {
	if len(taskID) < 20 || len(taskID) > 64 {
		return false
	}
	for _, char := range taskID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func sanitizeTaskError(err error) string {
	message := err.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return message
}

type countingReader struct {
	reader io.Reader
	onRead func(int64)
	total  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.total += int64(n)
	if n > 0 && r.onRead != nil {
		r.onRead(r.total)
	}
	return n, err
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

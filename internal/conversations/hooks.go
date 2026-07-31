package conversations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mnemoo/cli/internal/stake"
)

const (
	defaultHookTimeout = 10 * time.Second
	maxHookTimeout     = 5 * time.Minute
	maxHookOutput      = 32 * 1024
)

type Hook struct {
	Name             string   `json:"name"`
	Events           []string `json:"events,omitempty"`
	Command          []string `json:"command,omitempty"`
	WebhookURL       string   `json:"webhookUrl,omitempty"`
	WebhookSecretEnv string   `json:"webhookSecretEnv,omitempty"`
	Teams            []string `json:"teams,omitempty"`
	Games            []string `json:"games,omitempty"`
	Disabled         bool     `json:"disabled,omitempty"`
	IncludePrivate   bool     `json:"includePrivate,omitempty"`
	IncludeOwn       bool     `json:"includeOwn,omitempty"`
	IncludeThread    bool     `json:"includeThread,omitempty"`
	TimeoutSeconds   int      `json:"timeoutSeconds,omitempty"`
}

type HookConfig struct {
	Hooks []Hook `json:"hooks"`
}

type HookPayload struct {
	Event     string                      `json:"event"`
	EventType string                      `json:"eventType"`
	AccountID string                      `json:"accountId"`
	Team      string                      `json:"team"`
	Game      string                      `json:"game"`
	GameName  string                      `json:"gameName"`
	Message   stake.ApprovalEvent         `json:"message"`
	Stats     stake.MessageStats          `json:"stats"`
	Thread    *stake.ApprovalConversation `json:"thread,omitempty"`
}

type HookResult struct {
	Hook       string `json:"hook"`
	EventID    int64  `json:"eventId"`
	Success    bool   `json:"success"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMs"`
}

type HookRunner struct {
	path   string
	client *http.Client
	mu     sync.Mutex
}

func NewHookRunner(path string) *HookRunner {
	return &HookRunner{path: path, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (r *HookRunner) Path() string { return r.path }

func (r *HookRunner) Load() ([]Hook, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading hooks config: %w", err)
	}
	var config HookConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing hooks config: %w", err)
	}
	return validateHooks(config.Hooks)
}

func (r *HookRunner) Save(hooks []Hook) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := acquireFileLock(r.path, "hooks config")
	if err != nil {
		return err
	}
	defer releaseFileLock(lock)
	return r.save(hooks)
}

func (r *HookRunner) save(hooks []Hook) error {
	validated, err := validateHooks(hooks)
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".hooks-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary hooks config: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("securing hooks config: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(HookConfig{Hooks: validated}); err != nil {
		_ = file.Close()
		return fmt.Errorf("encoding hooks config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("syncing hooks config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing hooks config: %w", err)
	}
	if err := replaceFile(tempPath, r.path); err != nil {
		return fmt.Errorf("installing hooks config: %w", err)
	}
	return nil
}

func (r *HookRunner) Upsert(hook Hook) ([]Hook, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := acquireFileLock(r.path, "hooks config")
	if err != nil {
		return nil, err
	}
	defer releaseFileLock(lock)

	hooks, err := r.Load()
	if err != nil {
		return nil, err
	}
	replaced := false
	for index := range hooks {
		if hooks[index].Name == strings.TrimSpace(hook.Name) {
			hooks[index] = hook
			replaced = true
			break
		}
	}
	if !replaced {
		hooks = append(hooks, hook)
	}
	if err := r.save(hooks); err != nil {
		return nil, err
	}
	return r.Load()
}

func (r *HookRunner) Delete(name string) ([]Hook, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := acquireFileLock(r.path, "hooks config")
	if err != nil {
		return nil, err
	}
	defer releaseFileLock(lock)

	hooks, err := r.Load()
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	filtered := make([]Hook, 0, len(hooks))
	found := false
	for _, hook := range hooks {
		if hook.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, hook)
	}
	if !found {
		return nil, fmt.Errorf("hook %q not found", name)
	}
	if err := r.save(filtered); err != nil {
		return nil, err
	}
	return r.Load()
}

func validateHooks(hooks []Hook) ([]Hook, error) {
	validated := append([]Hook(nil), hooks...)
	names := make(map[string]struct{}, len(validated))
	for index := range validated {
		hook := &validated[index]
		hook.Name = strings.TrimSpace(hook.Name)
		if hook.Name == "" {
			return nil, fmt.Errorf("hook %d has no name", index+1)
		}
		if _, exists := names[hook.Name]; exists {
			return nil, fmt.Errorf("duplicate hook name %q", hook.Name)
		}
		names[hook.Name] = struct{}{}
		hasCommand := len(hook.Command) > 0 && strings.TrimSpace(hook.Command[0]) != ""
		hook.WebhookURL = strings.TrimSpace(hook.WebhookURL)
		hasWebhook := hook.WebhookURL != ""
		if hasCommand == hasWebhook {
			return nil, fmt.Errorf("hook %q must define exactly one of command or webhookUrl", hook.Name)
		}
		if hasWebhook {
			if err := validateWebhookURL(hook.WebhookURL); err != nil {
				return nil, fmt.Errorf("hook %q: %w", hook.Name, err)
			}
			hook.WebhookSecretEnv = strings.TrimSpace(hook.WebhookSecretEnv)
		}
		if len(hook.Events) == 0 {
			hook.Events = []string{"new_message"}
		}
		if hook.TimeoutSeconds < 0 || time.Duration(hook.TimeoutSeconds)*time.Second > maxHookTimeout {
			return nil, fmt.Errorf("hook %q timeout must be between 0 and %d seconds", hook.Name, int(maxHookTimeout/time.Second))
		}
	}
	return validated, nil
}

func (r *HookRunner) Matching(hooks []Hook, payload HookPayload) []Hook {
	matched := make([]Hook, 0, len(hooks))
	for _, hook := range hooks {
		if hook.Disabled || !matchesHookEvent(hook.Events, payload) {
			continue
		}
		if len(hook.Teams) > 0 && !slices.Contains(hook.Teams, payload.Team) {
			continue
		}
		if len(hook.Games) > 0 && !slices.Contains(hook.Games, payload.Game) {
			continue
		}
		if payload.Message.IsPrivate() && !hook.IncludePrivate {
			continue
		}
		if payload.Message.User.ID != "" && payload.Message.User.ID == payload.AccountID && !hook.IncludeOwn {
			continue
		}
		matched = append(matched, hook)
	}
	return matched
}

func (r *HookRunner) Run(ctx context.Context, hook Hook, payload HookPayload) (result HookResult) {
	result = HookResult{Hook: hook.Name, EventID: payload.Message.ID}
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	data, err := json.Marshal(payload)
	if err != nil {
		result.Error = fmt.Sprintf("encoding hook payload: %v", err)
		return result
	}
	timeout := defaultHookTimeout
	if hook.TimeoutSeconds > 0 {
		timeout = time.Duration(hook.TimeoutSeconds) * time.Second
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if hook.WebhookURL != "" {
		return r.runWebhook(hookCtx, hook, payload, data, result, started)
	}
	command := exec.CommandContext(hookCtx, hook.Command[0], hook.Command[1:]...)
	command.Stdin = bytes.NewReader(data)
	command.Env = append(sanitizedEnvironment(),
		"STAKE_HOOK_EVENT="+payload.Event,
		"STAKE_HOOK_NAME="+hook.Name,
		"STAKE_TEAM="+payload.Team,
		"STAKE_GAME="+payload.Game,
		fmt.Sprintf("STAKE_MESSAGE_ID=%d", payload.Message.ID),
	)
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	result.Output = strings.TrimSpace(output.String())
	if err != nil {
		if errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
			result.Error = fmt.Sprintf("hook timed out after %s", timeout)
		} else {
			result.Error = err.Error()
		}
		return result
	}
	result.Success = true
	return result
}

func (r *HookRunner) runWebhook(ctx context.Context, hook Hook, payload HookPayload, data []byte, base HookResult, started time.Time) (result HookResult) {
	result = base
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.WebhookURL, bytes.NewReader(data))
	if err != nil {
		result.Error = fmt.Sprintf("creating webhook request: %v", err)
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "stakecli-review-hook/1")
	request.Header.Set("X-StakeCLI-Event", payload.Event)
	request.Header.Set("X-StakeCLI-Event-Type", payload.EventType)
	request.Header.Set("X-StakeCLI-Hook", hook.Name)
	request.Header.Set("X-StakeCLI-Delivery", fmt.Sprintf("%s/%s/%d", payload.Team, payload.Game, payload.Message.ID))
	if hook.WebhookSecretEnv != "" {
		secret := os.Getenv(hook.WebhookSecretEnv)
		if secret == "" {
			result.Error = fmt.Sprintf("webhook secret environment variable %s is not set", hook.WebhookSecretEnv)
			return result
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(data)
		request.Header.Set("X-StakeCLI-Signature-256", fmt.Sprintf("sha256=%x", mac.Sum(nil)))
	}
	response, err := r.client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxHookOutput))
	result.Output = strings.TrimSpace(string(body))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Error = fmt.Sprintf("webhook returned HTTP %d", response.StatusCode)
		return result
	}
	result.Success = true
	return result
}

func matchesHookEvent(events []string, payload HookPayload) bool {
	for _, event := range events {
		switch event {
		case payload.Event, payload.EventType:
			return true
		case "reviewer_message":
			if payload.Message.IsMessage() && payload.Message.User.ID != payload.AccountID {
				return true
			}
		case "public_message":
			if payload.Message.IsMessage() && !payload.Message.IsPrivate() {
				return true
			}
		case "private_message":
			if payload.Message.IsMessage() && payload.Message.IsPrivate() {
				return true
			}
		case "approval_event":
			if !payload.Message.IsMessage() {
				return true
			}
		case "*":
			return true
		}
	}
	return false
}

func validateWebhookURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("webhookUrl must be an absolute URL")
	}
	if parsed.User != nil {
		return errors.New("webhookUrl must not contain credentials")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("webhookUrl must use HTTPS (HTTP is allowed only for loopback testing)")
}

type limitedBuffer struct {
	buffer bytes.Buffer
	wrote  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := maxHookOutput - b.wrote
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		written, err := b.buffer.Write(data)
		b.wrote += written
		if err != nil {
			return written, err
		}
	}
	return original, nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

var _ io.Writer = (*limitedBuffer)(nil)

func sanitizedEnvironment() []string {
	sensitive := []string{"SID", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "COOKIE", "AUTH", "API_KEY", "APIKEY", "PRIVATE_KEY", "AWS_", "GITHUB_", "GITLAB_"}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		blocked := false
		for _, fragment := range sensitive {
			if strings.Contains(upper, fragment) {
				blocked = true
				break
			}
		}
		if !blocked {
			environment = append(environment, entry)
		}
	}
	return environment
}

package conversations

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mnemoo/cli/internal/stake"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestWebhookHookDeliversSignedReviewerEvent(t *testing.T) {
	t.Setenv("STAKE_TEST_WEBHOOK_SECRET", "test-secret")
	runner := NewHookRunner(t.TempDir() + "/hooks.json")
	var received bool
	runner.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = true
		if request.URL.String() != "https://hooks.example.test/stake" {
			t.Fatalf("unexpected webhook URL %s", request.URL)
		}
		if request.Header.Get("X-StakeCLI-Event") != "new_message" {
			t.Fatalf("unexpected event header %q", request.Header.Get("X-StakeCLI-Event"))
		}
		if !strings.HasPrefix(request.Header.Get("X-StakeCLI-Signature-256"), "sha256=") {
			t.Fatal("missing signed webhook header")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"game":"game"`) {
			t.Fatalf("unexpected webhook body %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})
	hook := Hook{
		Name: "webhook", Events: []string{"reviewer_message"},
		WebhookURL:       "https://hooks.example.test/stake",
		WebhookSecretEnv: "STAKE_TEST_WEBHOOK_SECRET",
	}
	payload := HookPayload{
		Event: "new_message", EventType: "public_comment", AccountID: "self",
		Team: "team", Game: "game", Message: stake.ApprovalEvent{
			ID: 42, Type: "public_comment", User: stake.ApprovalUser{ID: "reviewer"},
		},
	}
	result := runner.Run(context.Background(), hook, payload)
	if !received || !result.Success {
		t.Fatalf("webhook result: %#v", result)
	}
}

func TestWebhookHookValidationRequiresSafeSingleTarget(t *testing.T) {
	for _, hook := range []Hook{
		{Name: "none"},
		{Name: "both", Command: []string{"true"}, WebhookURL: "https://example.test/hook"},
		{Name: "unsafe", WebhookURL: "http://example.test/hook"},
		{Name: "credentials", WebhookURL: "https://user:pass@example.test/hook"},
	} {
		if _, err := validateHooks([]Hook{hook}); err == nil {
			t.Fatalf("expected hook validation error for %#v", hook)
		}
	}
	if _, err := validateHooks([]Hook{{
		Name: "loopback", WebhookURL: "http://127.0.0.1:9000/hook",
	}}); err != nil {
		t.Fatalf("loopback test webhook rejected: %v", err)
	}
}

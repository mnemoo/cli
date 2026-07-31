package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/service"
)

func TestServerListsTypedToolsAndCallsLocalTool(t *testing.T) {
	t.Setenv("STAKE_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(service.New(api.NewClient("")), AuthInfo{}, "test")
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.MCP().Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed.Tools) != 39 {
		t.Fatalf("got %d tools, want 39", len(listed.Tools))
	}

	tools := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{
		"auth_status", "get_game", "get_compliance_policy", "check_math_compliance", "plan_upload", "execute_upload", "start_upload", "list_upload_processes", "get_upload_process", "publish",
		"pause_upload", "resume_upload", "cancel_upload", "retry_upload",
		"get_team_analytics", "get_game_analytics",
		"list_conversations", "get_conversation", "set_conversation_read", "conversation_reply_guide",
		"prepare_review_reply", "send_review_reply", "list_message_hooks",
		"upsert_message_hook", "delete_message_hook", "test_message_hook",
		"daemon_status", "start_daemon", "stop_daemon",
		"mlr_inspect", "mlr_preview_change", "mlr_change",
	} {
		if tools[name] == nil {
			t.Errorf("missing tool %q", name)
		}
	}
	if annotations := tools["execute_upload"].Annotations; annotations == nil || annotations.ReadOnlyHint || annotations.DestructiveHint == nil || !*annotations.DestructiveHint {
		t.Fatalf("execute_upload annotations do not describe a destructive mutation: %#v", annotations)
	}
	if tools["plan_upload"].OutputSchema == nil {
		t.Fatal("plan_upload is missing its generated output schema")
	}

	policyResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_compliance_policy", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call get_compliance_policy: %v", err)
	}
	if policyResult.IsError {
		t.Fatalf("get_compliance_policy returned tool error: %#v", policyResult.Content)
	}
	policyOutput, ok := policyResult.StructuredContent.(map[string]any)
	if !ok || policyOutput["schema_version"] != "1.1" {
		t.Fatalf("unexpected policy output: %#v", policyResult.StructuredContent)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_upload_path",
		Arguments: map[string]any{"path": dir},
	})
	if err != nil {
		t.Fatalf("call validate_upload_path: %v", err)
	}
	if result.IsError {
		t.Fatalf("validate_upload_path returned tool error: %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected structured output type %T", result.StructuredContent)
	}
	if valid, _ := structured["valid"].(bool); !valid {
		t.Fatalf("expected valid path output, got %#v", structured)
	}

	mathDir := t.TempDir()
	for name, content := range map[string]string{
		"index.json":   `{"modes":[{"name":"base","cost":1,"events":"events.jsonl","weights":"weights.csv"}]}`,
		"events.jsonl": "{}\n",
		"weights.csv":  "0,900,0\n1,100,967\n",
	} {
		if err := os.WriteFile(filepath.Join(mathDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write math fixture %s: %v", name, err)
		}
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "check_math_compliance",
		Arguments: map[string]any{
			"path":       mathDir,
			"compliance": map[string]any{"base_mode": "base"},
		},
	})
	if err != nil {
		t.Fatalf("call check_math_compliance: %v", err)
	}
	if result.IsError {
		t.Fatalf("check_math_compliance returned tool error: %#v", result.Content)
	}
	structured, ok = result.StructuredContent.(map[string]any)
	compliance, complianceOK := structured["compliance"].(map[string]any)
	if !ok || !complianceOK || compliance["status"] != "pass" || compliance["complete"] != true {
		t.Fatalf("unexpected compliance output: %#v", result.StructuredContent)
	}

	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_upload_processes",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call list_upload_processes: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_upload_processes returned tool error: %#v", result.Content)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("server stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		cancel()
	}
}

func TestAuthStatusDoesNotExposeCredential(t *testing.T) {
	server := New(service.New(api.NewClient("super-secret")), AuthInfo{Configured: true, Source: "--key"}, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range listed.Tools {
		if containsSecret(tool.Description, "super-secret") {
			t.Fatalf("credential leaked in tool %q description", tool.Name)
		}
	}
}

func containsSecret(value, secret string) bool {
	if len(secret) == 0 || len(value) < len(secret) {
		return false
	}
	for i := 0; i <= len(value)-len(secret); i++ {
		if value[i:i+len(secret)] == secret {
			return true
		}
	}
	return false
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConversationEndpoints(t *testing.T) {
	var postedPath string
	var posted struct {
		Comment string   `json:"comment"`
		Assets  []string `json:"assets"`
	}
	var patched struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if cookie, err := request.Cookie("sid"); err != nil || cookie.Value != "session" {
			t.Errorf("missing session cookie: %v", err)
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/teams/team/games":
			if request.URL.Query().Get("has_approval") != "true" {
				t.Errorf("missing has_approval query: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`[{"name":"Game","slug":"game","published":true,"approval":{"open":true,"locked":false,"column":"new"}}]`))
		case request.Method == http.MethodGet && request.URL.Path == "/teams/team/approvals/game":
			_, _ = response.Write([]byte(`{"updated":2,"created":1,"eventCount":1,"mathVersion":1,"frontVersion":2,"open":true,"locked":false,"game":{"name":"Game","slug":"game"},"team":{"name":"Team","slug":"team","trustLevel":0},"events":[{"id":9,"time":2,"user":{"id":"reviewer","name":"Reviewer"},"type":"comment","data":{"comment":"hello","images":[]}}]}`))
		case request.Method == http.MethodPatch && request.URL.Path == "/teams/team/approvals/game":
			if err := json.NewDecoder(request.Body).Decode(&patched); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost:
			postedPath = request.URL.Path
			if err := json.NewDecoder(request.Body).Decode(&posted); err != nil {
				t.Errorf("decode post: %v", err)
			}
			response.WriteHeader(http.StatusOK)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	previous := baseURL
	baseURL = server.URL
	defer func() { baseURL = previous }()

	client := NewClient("session")
	games, err := client.ListApprovalGames(context.Background(), "team")
	if err != nil || len(games) != 1 || games[0].Slug != "game" {
		t.Fatalf("list approval games: games=%#v err=%v", games, err)
	}
	conversation, err := client.GetApprovalConversation(context.Background(), "team", "game")
	if err != nil || len(conversation.Events) != 1 || conversation.Events[0].ID != 9 {
		t.Fatalf("get conversation: conversation=%#v err=%v", conversation, err)
	}
	if err := client.PostApprovalComment(context.Background(), "team", "game", "reply", true, nil); err != nil {
		t.Fatalf("post private comment: %v", err)
	}
	if postedPath != "/teams/team/approvals/game/private" || posted.Comment != "reply" || posted.Assets == nil || len(posted.Assets) != 0 {
		t.Fatalf("unexpected post: path=%s body=%#v", postedPath, posted)
	}
	if err := client.UpdateApprovalVersion(context.Background(), "team", "game", "math", 12); err != nil {
		t.Fatalf("update approval version: %v", err)
	}
	if patched.Version != 12 || patched.Type != "math" {
		t.Fatalf("unexpected patch body: %#v", patched)
	}
}

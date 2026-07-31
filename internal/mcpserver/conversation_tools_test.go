package mcpserver

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mnemoo/cli/internal/stake"
)

func TestReviewReplyBlocksSensitiveMaterial(t *testing.T) {
	conversation := &stake.ApprovalConversation{Open: true}
	for _, message := range []string{
		"Authorization: Bearer secret-token",
		"sid=super-secret",
		"https://example.test/file?X-Amz-Signature=secret",
		"See ~/private/file.log",
	} {
		if _, blocked := reviewReplyWarnings(message, "public", conversation); blocked == "" {
			t.Errorf("sensitive reply was not blocked: %q", message)
		}
	}
}

func TestReviewReplyGuideCoversVisibilityAndEvidence(t *testing.T) {
	for _, required := range []string{"Public", "Private", "Do not claim", "SID/cookies", "prepare_review_reply"} {
		if !strings.Contains(reviewerReplyGuide, required) {
			t.Errorf("reply guide missing %q", required)
		}
	}
}

func TestPublicReviewReplyCannotCopyPrivateEvent(t *testing.T) {
	privateComment := "Internal root cause details must stay private."
	conversation := &stake.ApprovalConversation{Open: true, Events: []stake.ApprovalEvent{{
		ID: 1, Type: "private_comment", Data: stake.ApprovalEventData{Comment: &privateComment, Images: []string{"private-asset-id"}},
	}}}
	if _, blocked := reviewReplyWarnings("Reviewer update: "+privateComment, "public", conversation); blocked == "" {
		t.Fatal("public reply copied private comment without being blocked")
	}
	if _, blocked := reviewReplyWarnings("See private-asset-id", "public", conversation); blocked == "" {
		t.Fatal("public reply exposed private asset without being blocked")
	}
	if _, blocked := reviewReplyWarnings("Internal note", "private", conversation); blocked != "" {
		t.Fatalf("private reply was unexpectedly blocked: %s", blocked)
	}
}

func TestReplyDraftCanOnlyBeClaimedOnce(t *testing.T) {
	server := &Server{drafts: map[string]replyDraft{
		"draft": {ID: "draft", CreatedAt: time.Now().UnixMilli()},
	}}
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, exists := server.takeReplyDraft("draft")
			results <- exists
		}()
	}
	wait.Wait()
	close(results)
	claimed := 0
	for exists := range results {
		if exists {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("draft was claimed %d times, want exactly once", claimed)
	}
}

func TestExpiredReplyDraftCannotBeClaimed(t *testing.T) {
	server := &Server{drafts: map[string]replyDraft{
		"expired": {ID: "expired", CreatedAt: time.Now().Add(-replyDraftTTL - time.Second).UnixMilli()},
	}}
	if _, exists := server.takeReplyDraft("expired"); exists {
		t.Fatal("expired draft was accepted")
	}
}

package service

import (
	"context"
	"testing"

	"github.com/mnemoo/cli/internal/stake"
)

type approvalVersionGateway struct {
	planningGateway
	conversation *stake.ApprovalConversation
	updateCalls  int
	updateType   string
	updateValue  int
}

func (g *approvalVersionGateway) ListApprovalGames(context.Context, string) ([]stake.Game, error) {
	return nil, nil
}

func (g *approvalVersionGateway) GetApprovalConversation(context.Context, string, string) (*stake.ApprovalConversation, error) {
	return g.conversation, nil
}

func (g *approvalVersionGateway) PostApprovalComment(context.Context, string, string, string, bool, []string) error {
	return nil
}

func (g *approvalVersionGateway) UpdateApprovalVersion(_ context.Context, _, _, kind string, version int) error {
	g.updateCalls++
	g.updateType = kind
	g.updateValue = version
	return nil
}

func TestUpdateOpenApprovalVersionUsesPublishedVersion(t *testing.T) {
	gateway := &approvalVersionGateway{
		conversation: &stake.ApprovalConversation{Open: true, MathVersion: 4, FrontVersion: 8},
	}
	result, err := New(gateway).UpdateOpenApprovalVersion(context.Background(), "team", "game", "math", 5)
	if err != nil {
		t.Fatalf("update open approval: %v", err)
	}
	if !result.Open || !result.Updated || result.PreviousVersion != 4 || result.TargetVersion != 5 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if gateway.updateCalls != 1 || gateway.updateType != "math" || gateway.updateValue != 5 {
		t.Fatalf("unexpected update call: calls=%d type=%q version=%d", gateway.updateCalls, gateway.updateType, gateway.updateValue)
	}
}

func TestUpdateOpenApprovalVersionClosedIsWarningOnly(t *testing.T) {
	gateway := &approvalVersionGateway{
		conversation: &stake.ApprovalConversation{Open: false, FrontVersion: 8},
	}
	result, err := New(gateway).UpdateOpenApprovalVersion(context.Background(), "team", "game", "front", 9)
	if err != nil {
		t.Fatalf("closed approval must be a no-op: %v", err)
	}
	if result.Open || result.Updated || result.Warning == "" || result.PreviousVersion != 8 || result.TargetVersion != 9 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if gateway.updateCalls != 0 {
		t.Fatalf("closed approval made %d update calls", gateway.updateCalls)
	}
}

func TestUpdateOpenApprovalVersionAlreadyCurrentSkipsPatch(t *testing.T) {
	gateway := &approvalVersionGateway{
		conversation: &stake.ApprovalConversation{Open: true, MathVersion: 5},
	}
	result, err := New(gateway).UpdateOpenApprovalVersion(context.Background(), "team", "game", "math", 5)
	if err != nil {
		t.Fatalf("already-current approval: %v", err)
	}
	if !result.Open || result.Updated || result.Warning != "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if gateway.updateCalls != 0 {
		t.Fatalf("already-current approval made %d update calls", gateway.updateCalls)
	}
}

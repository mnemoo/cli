package service

import (
	"context"
	"errors"
	"strings"

	"github.com/mnemoo/cli/internal/stake"
)

// ConversationGateway is optional so existing upload-only Gateway fakes do
// not need to implement unrelated conversation methods.
type ConversationGateway interface {
	ListApprovalGames(context.Context, string) ([]stake.Game, error)
	GetApprovalConversation(context.Context, string, string) (*stake.ApprovalConversation, error)
	PostApprovalComment(context.Context, string, string, string, bool, []string) error
}

type ApprovalVersionGateway interface {
	UpdateApprovalVersion(context.Context, string, string, string, int) error
}

func (s *Service) conversationGateway() (ConversationGateway, error) {
	gateway, ok := s.gateway.(ConversationGateway)
	if !ok {
		return nil, errors.New("conversation operations are not supported by this gateway")
	}
	return gateway, nil
}

func (s *Service) ListApprovalGames(ctx context.Context, team string) ([]stake.Game, error) {
	if strings.TrimSpace(team) == "" {
		return nil, errors.New("team is required")
	}
	gateway, err := s.conversationGateway()
	if err != nil {
		return nil, err
	}
	return gateway.ListApprovalGames(ctx, team)
}

func (s *Service) GetConversation(ctx context.Context, team, game string) (*stake.ApprovalConversation, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	gateway, err := s.conversationGateway()
	if err != nil {
		return nil, err
	}
	return gateway.GetApprovalConversation(ctx, team, game)
}

func (s *Service) SendMessage(ctx context.Context, team, game, comment string, private bool, assets []string) error {
	if err := validateGameRef(team, game); err != nil {
		return err
	}
	if strings.TrimSpace(comment) == "" && len(assets) == 0 {
		return errors.New("comment or at least one asset is required")
	}
	gateway, err := s.conversationGateway()
	if err != nil {
		return err
	}
	return gateway.PostApprovalComment(ctx, team, game, comment, private, assets)
}

// UpdateOpenApprovalVersion reads the current approval first and updates only
// an open approval. A closed approval is an expected no-op so publishing can
// remain successful when the opt-in synchronization flag is used.
func (s *Service) UpdateOpenApprovalVersion(ctx context.Context, team, game, kind string, version int) (*stake.ApprovalVersionUpdate, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	if kind != "math" && kind != "front" {
		return nil, errors.New("approval version type must be math or front")
	}
	if version < 0 {
		return nil, errors.New("approval version must be non-negative")
	}

	conversation, err := s.GetConversation(ctx, team, game)
	if err != nil {
		return nil, err
	}
	previous := conversation.MathVersion
	if kind == "front" {
		previous = conversation.FrontVersion
	}
	result := &stake.ApprovalVersionUpdate{
		Type:            kind,
		PreviousVersion: previous,
		TargetVersion:   version,
		Open:            conversation.Open,
	}
	if !conversation.Open {
		result.Warning = "approval is closed; version was not changed"
		return result, nil
	}
	if previous == version {
		return result, nil
	}

	gateway, ok := s.gateway.(ApprovalVersionGateway)
	if !ok {
		return nil, errors.New("approval version updates are not supported by this gateway")
	}
	if err := gateway.UpdateApprovalVersion(ctx, team, game, kind, version); err != nil {
		return nil, err
	}
	result.Updated = true
	return result, nil
}

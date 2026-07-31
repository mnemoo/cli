package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/mnemoo/cli/internal/stake"
)

// ListApprovalGames returns games that currently have an approval thread.
func (c *Client) ListApprovalGames(ctx context.Context, team string) ([]stake.Game, error) {
	var games []stake.Game
	endpoint := fmt.Sprintf("%s/teams/%s/games?has_approval=true", baseURL, url.PathEscape(team))
	if err := c.getJSON(ctx, endpoint, &games); err != nil {
		return nil, fmt.Errorf("list approval games: %w", err)
	}
	return games, nil
}

func (c *Client) GetApprovalConversation(ctx context.Context, team, game string) (*stake.ApprovalConversation, error) {
	var conversation stake.ApprovalConversation
	endpoint := fmt.Sprintf("%s/teams/%s/approvals/%s", baseURL, url.PathEscape(team), url.PathEscape(game))
	if err := c.getJSON(ctx, endpoint, &conversation); err != nil {
		return nil, fmt.Errorf("get approval conversation: %w", err)
	}
	return &conversation, nil
}

// UpdateApprovalVersion changes the math or front version attached to an
// existing approval. The Stake portal uses PATCH on the approval itself for
// this operation; /update is reserved for changing the approval column.
func (c *Client) UpdateApprovalVersion(ctx context.Context, team, game, kind string, version int) error {
	endpoint := fmt.Sprintf("%s/teams/%s/approvals/%s", baseURL, url.PathEscape(team), url.PathEscape(game))
	body := struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}{Version: version, Type: kind}
	if err := c.patchJSON(ctx, endpoint, body, nil); err != nil {
		return fmt.Errorf("update approval version: %w", err)
	}
	return nil
}

func (c *Client) PostApprovalComment(ctx context.Context, team, game, comment string, private bool, assets []string) error {
	comment = strings.TrimSpace(comment)
	if comment == "" && len(assets) == 0 {
		return fmt.Errorf("comment or at least one asset is required")
	}
	suffix := "comment"
	if private {
		suffix = "private"
	}
	endpoint := fmt.Sprintf("%s/teams/%s/approvals/%s/%s", baseURL, url.PathEscape(team), url.PathEscape(game), suffix)
	body := struct {
		Comment string   `json:"comment,omitempty"`
		Assets  []string `json:"assets"`
	}{Comment: comment, Assets: assets}
	if body.Assets == nil {
		body.Assets = []string{}
	}
	if err := c.postJSON(ctx, endpoint, body, nil); err != nil {
		return fmt.Errorf("post approval comment: %w", err)
	}
	return nil
}

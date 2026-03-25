package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const baseURL = "https://stake-engine.com/api"

type Client struct {
	sid    string
	http   *http.Client
}

func NewClient(sid string) *Client {
	return &Client{
		sid:  sid,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) ListTeams(ctx context.Context) ([]TeamListItem, error) {
	var teams []TeamListItem
	if err := c.getJSON(ctx, baseURL+"/teams", &teams); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return teams, nil
}

func (c *Client) ListTeamGames(ctx context.Context, teamSlug string) ([]TeamGameCard, error) {
	var games []TeamGameCard
	url := fmt.Sprintf("%s/teams/%s/games", baseURL, teamSlug)
	if err := c.getJSON(ctx, url, &games); err != nil {
		return nil, fmt.Errorf("list games: %w", err)
	}
	return games, nil
}

func (c *Client) GetGameDetail(ctx context.Context, teamSlug, gameSlug string) (*TeamGameDetail, error) {
	var detail TeamGameDetail
	url := fmt.Sprintf("%s/teams/%s/games/%s", baseURL, teamSlug, gameSlug)
	if err := c.getJSON(ctx, url, &detail); err != nil {
		return nil, fmt.Errorf("game detail: %w", err)
	}
	return &detail, nil
}

func (c *Client) GetGameVersions(ctx context.Context, teamSlug, gameSlug string) ([]GameVersionHistoryItem, error) {
	var versions []GameVersionHistoryItem
	url := fmt.Sprintf("%s/teams/%s/games/%s/versions", baseURL, teamSlug, gameSlug)
	if err := c.getJSON(ctx, url, &versions); err != nil {
		return nil, fmt.Errorf("game versions: %w", err)
	}
	return versions, nil
}

func (c *Client) GetTeamBalance(ctx context.Context, teamSlug string) (*TeamBalance, error) {
	var balance TeamBalance
	url := fmt.Sprintf("%s/teams/%s/balance", baseURL, teamSlug)
	if err := c.getJSON(ctx, url, &balance); err != nil {
		return nil, fmt.Errorf("team balance: %w", err)
	}
	return &balance, nil
}

func (c *Client) GetGameStats(ctx context.Context, teamSlug, gameSlug string) (*GameStatsByModeResponse, error) {
	var stats GameStatsByModeResponse
	url := fmt.Sprintf("%s/teams/%s/games/%s/stats", baseURL, teamSlug, gameSlug)
	if err := c.getJSON(ctx, url, &stats); err != nil {
		return nil, fmt.Errorf("game stats: %w", err)
	}
	return &stats, nil
}

func (c *Client) getJSON(ctx context.Context, url string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "sid", Value: c.sid})

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("session expired (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

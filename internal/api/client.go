package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

const baseURL = "https://stake-engine.com/api"

type Client struct {
	sid    string
	http   *http.Client
	s3http *http.Client
}

func NewClient(sid string) *Client {
	return &Client{
		sid:    sid,
		http:   &http.Client{Timeout: 15 * time.Second},
		s3http: &http.Client{},
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

func (c *Client) postJSON(ctx context.Context, url string, body any, dest any) error {
	return c.doPostJSON(ctx, c.http, url, body, dest)
}

func (c *Client) postJSONNoTimeout(ctx context.Context, url string, body any, dest any) error {
	return c.doPostJSON(ctx, c.s3http, url, body, dest)
}

func (c *Client) doPostJSON(ctx context.Context, hc *http.Client, url string, body any, dest any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "sid", Value: c.sid})

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("session expired (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

func (c *Client) putBytes(ctx context.Context, url string, data []byte, contentType string) error {
	return c.putBytesWithCounter(ctx, url, data, contentType, nil)
}

func (c *Client) putBytesWithCounter(ctx context.Context, url string, data []byte, contentType string, counter *atomic.Int64) error {
	var body io.Reader = bytes.NewReader(data)
	if counter != nil {
		body = &countingReader{r: body, n: counter}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return fmt.Errorf("creating PUT request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(data))

	resp, err := c.s3http.Do(req)
	if err != nil {
		return fmt.Errorf("S3 upload failed (%s, %d bytes): %w", contentType, len(data), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("S3 returned HTTP %d (%d bytes uploaded): %s",
			resp.StatusCode, len(data), string(respBody))
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.n.Add(int64(n))
	}
	return n, err
}

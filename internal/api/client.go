package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"time"

	"github.com/mnemoo/cli/internal/stake"
)

var baseURL = getBaseURL()

const (
	maxErrorBodySize        = 64 * 1024
	s3MaxConnections        = 32
	s3SocketWriteBufferSize = 512 * 1024
	s3ResponseHeaderTimeout = 10_000 * time.Second
)

func getBaseURL() string {
	if u := os.Getenv("STAKE_API_URL"); u != "" {
		return u
	}
	return "https://studio.engine.io/api"
}

type Client struct {
	sid      string
	http     *http.Client
	s3http   *http.Client
	longHTTP *http.Client
}

func NewClient(sid string) *Client {
	s3Transport := http.DefaultTransport.(*http.Transport).Clone()
	s3Transport.MaxIdleConns = 100
	s3Transport.MaxIdleConnsPerHost = s3MaxConnections
	s3Transport.MaxConnsPerHost = s3MaxConnections
	s3Transport.ResponseHeaderTimeout = s3ResponseHeaderTimeout
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	s3Transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			if err := tcpConn.SetWriteBuffer(s3SocketWriteBufferSize); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("setting S3 TCP write buffer: %w", err)
			}
		}
		return conn, nil
	}
	return &Client{
		sid:      sid,
		http:     &http.Client{Timeout: 15 * time.Second},
		s3http:   &http.Client{Transport: s3Transport},
		longHTTP: &http.Client{},
	}
}

func (c *Client) GetCurrentUser(ctx context.Context) (*stake.User, error) {
	var user stake.User
	if err := c.getJSON(ctx, baseURL+"/users", &user); err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	return &user, nil
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

func (c *Client) GetTeamStats(ctx context.Context, teamSlug string, start, end time.Time) ([]TeamOperatorStat, error) {
	var stats []TeamOperatorStat
	query := url.Values{
		"operator": {"stake,stake_us,stake_mx"},
		"start":    {fmt.Sprintf("%d", start.Unix())},
		"end":      {fmt.Sprintf("%d", end.Unix())},
	}
	endpoint := fmt.Sprintf("%s/teams/%s/teamStats?%s", baseURL, teamSlug, query.Encode())
	if err := c.getJSON(ctx, endpoint, &stats); err != nil {
		return nil, fmt.Errorf("team stats: %w", err)
	}
	return stats, nil
}

func (c *Client) GetPaymentMethod(ctx context.Context, teamSlug string) (*PaymentMethod, error) {
	var method PaymentMethod
	endpoint := fmt.Sprintf("%s/teams/%s/payments/method", baseURL, teamSlug)
	if err := c.getJSON(ctx, endpoint, &method); err != nil {
		return nil, fmt.Errorf("payment method: %w", err)
	}
	return &method, nil
}

func (c *Client) GetGameStats(ctx context.Context, teamSlug, gameSlug string) (*GameStatsByModeResponse, error) {
	return c.getGameStats(ctx, teamSlug, gameSlug, nil)
}

func (c *Client) GetGameStatsRange(ctx context.Context, teamSlug, gameSlug string, start, end time.Time) (*GameStatsByModeResponse, error) {
	query := url.Values{
		"operator": {"stake,stake_us,stake_mx"},
		"start":    {fmt.Sprintf("%d", start.Unix())},
		"end":      {fmt.Sprintf("%d", end.Unix())},
	}
	return c.getGameStats(ctx, teamSlug, gameSlug, query)
}

func (c *Client) getGameStats(ctx context.Context, teamSlug, gameSlug string, query url.Values) (*GameStatsByModeResponse, error) {
	var stats GameStatsByModeResponse
	endpoint := fmt.Sprintf("%s/teams/%s/games/%s/stats", baseURL, teamSlug, gameSlug)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	if err := c.getJSON(ctx, endpoint, &stats); err != nil {
		return nil, fmt.Errorf("game stats: %w", err)
	}
	return &stats, nil
}

func (c *Client) GetGameGraph(ctx context.Context, teamSlug, gameSlug string, start, end time.Time) (*StatsGraphResponse, error) {
	var graph StatsGraphResponse
	query := url.Values{
		"game":     {gameSlug},
		"operator": {"stake,stake_us,stake_mx"},
		"start":    {fmt.Sprintf("%d", start.Unix())},
		"end":      {fmt.Sprintf("%d", end.Unix())},
	}
	endpoint := fmt.Sprintf("%s/teams/%s/graph?%s", baseURL, teamSlug, query.Encode())
	if err := c.getJSON(ctx, endpoint, &graph); err != nil {
		return nil, fmt.Errorf("game graph: %w", err)
	}
	return &graph, nil
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
	defer func() { _ = resp.Body.Close() }()

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
	return c.doJSON(ctx, c.http, http.MethodPost, url, body, dest)
}

func (c *Client) postJSONNoTimeout(ctx context.Context, url string, body any, dest any) error {
	return c.doJSON(ctx, c.longHTTP, http.MethodPost, url, body, dest)
}

func (c *Client) patchJSON(ctx context.Context, url string, body any, dest any) error {
	return c.doJSON(ctx, c.http, http.MethodPatch, url, body, dest)
}

func (c *Client) doJSON(ctx context.Context, hc *http.Client, method, url string, body any, dest any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "sid", Value: c.sid})

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("session expired (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

func (c *Client) putReader(ctx context.Context, url string, body io.Reader, size int64, contentType string, onRead func(int64), onBodySent func()) error {
	if onRead != nil {
		body = &countingReader{r: body, onRead: onRead}
	}
	if onBodySent != nil {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					onBodySent()
				}
			},
		})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return fmt.Errorf("creating PUT request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = size

	resp, err := c.s3http.Do(req)
	if err != nil {
		return &S3UploadError{Size: size, Err: sanitizeS3TransportError(err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &S3UploadError{StatusCode: resp.StatusCode, Size: size, Body: readErrorBody(resp.Body)}
	}
	return nil
}

// S3UploadError preserves the HTTP status so retry policy can distinguish
// transient failures from permanent presigned-request errors.
type S3UploadError struct {
	StatusCode int
	Size       int64
	Body       string
	Err        error
}

func (e *S3UploadError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("S3 upload failed (%d bytes): %v", e.Size, e.Err)
	}
	return fmt.Sprintf("S3 returned HTTP %d (%d bytes): %s", e.StatusCode, e.Size, e.Body)
}

func (e *S3UploadError) Unwrap() error { return e.Err }

func sanitizeS3TransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return fmt.Errorf("%s request: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

func readErrorBody(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxErrorBodySize+1))
	if len(data) <= maxErrorBodySize {
		return string(data)
	}
	return string(data[:maxErrorBodySize]) + "\n...[response truncated after 64 KiB]"
}

type countingReader struct {
	r      io.Reader
	onRead func(int64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.onRead(int64(n))
	}
	return n, err
}

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

type emptyInput struct{}

type authStatusOutput struct {
	Configured bool        `json:"configured"`
	Source     string      `json:"source,omitempty"`
	Valid      bool        `json:"valid"`
	User       *stake.User `json:"user,omitempty"`
	Message    string      `json:"message,omitempty"`
}

type listTeamsInput struct {
	IncludeBalances bool `json:"include_balances,omitempty" jsonschema:"include each team's current balance"`
}

type listTeamsOutput struct {
	Teams     []stake.Team           `json:"teams,omitempty"`
	Overviews []service.TeamOverview `json:"overviews,omitempty"`
}

type teamInput struct {
	Team string `json:"team" jsonschema:"Stake Engine team slug"`
}

type gameInput struct {
	Team string `json:"team" jsonschema:"Stake Engine team slug"`
	Game string `json:"game" jsonschema:"Stake Engine game slug"`
}

type scratchInput struct {
	Team string `json:"team" jsonschema:"Stake Engine team slug"`
	Game string `json:"game" jsonschema:"Stake Engine game slug"`
	Type string `json:"type" jsonschema:"scratch asset type: math or front"`
}

type gamesOutput struct {
	Games []stake.Game `json:"games"`
}

type gameOutput struct {
	Game *service.GameSnapshot `json:"game"`
}

type versionsOutput struct {
	Versions []stake.GameVersion `json:"versions"`
}

type statsOutput struct {
	Stats *stake.GameStats `json:"stats"`
}

type balanceOutput struct {
	Balance *stake.TeamBalance `json:"balance"`
}

type scratchOutput struct {
	Files []stake.ScratchObject `json:"files"`
}

type teamAnalyticsInput struct {
	Team  string `json:"team" jsonschema:"Stake Engine team slug"`
	Start string `json:"start,omitempty" jsonschema:"RFC3339 range start; defaults to 30 days before end"`
	End   string `json:"end,omitempty" jsonschema:"RFC3339 range end; defaults to now"`
}

type teamAnalyticsOutput struct {
	Team          string                   `json:"team"`
	Start         time.Time                `json:"start"`
	End           time.Time                `json:"end"`
	Balance       *stake.TeamBalance       `json:"balance"`
	PaymentMethod *stake.PaymentMethod     `json:"payment_method"`
	Operators     []stake.TeamOperatorStat `json:"operators"`
}

type gameAnalyticsInput struct {
	Team  string `json:"team" jsonschema:"Stake Engine team slug"`
	Game  string `json:"game" jsonschema:"Stake Engine game slug"`
	Start string `json:"start,omitempty" jsonschema:"RFC3339 range start; defaults to 30 days before end"`
	End   string `json:"end,omitempty" jsonschema:"RFC3339 range end; defaults to now"`
}

type gameAnalyticsOutput struct {
	Team  string            `json:"team"`
	Game  string            `json:"game"`
	Start time.Time         `json:"start"`
	End   time.Time         `json:"end"`
	Stats *stake.GameStats  `json:"stats"`
	Graph *stake.StatsGraph `json:"graph"`
}

func (s *Server) registerReadTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "auth_status", Title: "Stake authentication status",
		Description: "Check whether the MCP server has a Stake credential and whether the session is valid. Never returns the credential.",
		Annotations: readOnlyAnnotations("Stake authentication status", true),
	}, s.authStatus)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_teams", Title: "List Stake teams",
		Description: "List teams available to the authenticated Stake account, optionally including balances.",
		Annotations: readOnlyAnnotations("List Stake teams", true),
	}, s.listTeams)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_games", Title: "List team games",
		Description: "List all games for a Stake team.",
		Annotations: readOnlyAnnotations("List team games", true),
	}, s.listGames)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_game", Title: "Get game snapshot",
		Description: "Get game details, version history, and per-mode statistics in one snapshot.",
		Annotations: readOnlyAnnotations("Get game snapshot", true),
	}, s.getGame)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_game_versions", Title: "List game versions",
		Description: "List math and front-end versions for a game, newest first.",
		Annotations: readOnlyAnnotations("List game versions", true),
	}, s.listGameVersions)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_game_stats", Title: "Get game statistics",
		Description: "Get per-mode runtime and RTP statistics for a game.",
		Annotations: readOnlyAnnotations("Get game statistics", true),
	}, s.getGameStats)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_team_balance", Title: "Get team balance",
		Description: "Get the current balance and carry for a Stake team.",
		Annotations: readOnlyAnnotations("Get team balance", true),
	}, s.getTeamBalance)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_team_analytics", Title: "Get team analytics",
		Description: "Get operator-level bets, turnover, profit, expected profit, unique users, balance, and payment model for a time range.",
		Annotations: readOnlyAnnotations("Get team analytics", true),
	}, s.getTeamAnalytics)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_game_analytics", Title: "Get game analytics",
		Description: "Get time-ranged per-mode game statistics and graph buckets for profit, turnover, and bet count.",
		Annotations: readOnlyAnnotations("Get game analytics", true),
	}, s.getGameAnalytics)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_scratch_files", Title: "List scratch files",
		Description: "List remote math or front-end files currently staged in a game's scratch area.",
		Annotations: readOnlyAnnotations("List scratch files", true),
	}, s.listScratchFiles)
}

func (s *Server) authStatus(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, authStatusOutput, error) {
	output := authStatusOutput{Configured: s.auth.Configured, Source: s.auth.Source}
	if !s.auth.Configured {
		output.Message = "No credential configured. Set STAKE_SID, use the keyring, or pass --sid/--key when starting stakecli mcp."
		return nil, output, nil
	}
	user, err := s.service.CurrentUser(ctx)
	if err != nil {
		output.Message = err.Error()
		return nil, output, nil
	}
	output.Valid = true
	output.User = user
	return nil, output, nil
}

func (s *Server) requireAuth() error {
	if !s.auth.Configured {
		return errors.New("stake credential is not configured; set STAKE_SID, use the keyring, or start stakecli mcp with --sid/--key")
	}
	return nil
}

func (s *Server) listTeams(ctx context.Context, _ *mcp.CallToolRequest, input listTeamsInput) (*mcp.CallToolResult, listTeamsOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, listTeamsOutput{}, err
	}
	if input.IncludeBalances {
		overviews, err := s.service.ListTeamOverviews(ctx)
		return nil, listTeamsOutput{Overviews: overviews}, err
	}
	teams, err := s.service.ListTeams(ctx)
	return nil, listTeamsOutput{Teams: teams}, err
}

func (s *Server) listGames(ctx context.Context, _ *mcp.CallToolRequest, input teamInput) (*mcp.CallToolResult, gamesOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, gamesOutput{}, err
	}
	games, err := s.service.ListGames(ctx, input.Team)
	return nil, gamesOutput{Games: games}, err
}

func (s *Server) getGame(ctx context.Context, _ *mcp.CallToolRequest, input gameInput) (*mcp.CallToolResult, gameOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, gameOutput{}, err
	}
	game, err := s.service.GetGameSnapshot(ctx, input.Team, input.Game)
	return nil, gameOutput{Game: game}, err
}

func (s *Server) listGameVersions(ctx context.Context, _ *mcp.CallToolRequest, input gameInput) (*mcp.CallToolResult, versionsOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, versionsOutput{}, err
	}
	versions, err := s.service.ListGameVersions(ctx, input.Team, input.Game)
	return nil, versionsOutput{Versions: versions}, err
}

func (s *Server) getGameStats(ctx context.Context, _ *mcp.CallToolRequest, input gameInput) (*mcp.CallToolResult, statsOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, statsOutput{}, err
	}
	stats, err := s.service.GetGameStats(ctx, input.Team, input.Game)
	return nil, statsOutput{Stats: stats}, err
}

func (s *Server) getTeamBalance(ctx context.Context, _ *mcp.CallToolRequest, input teamInput) (*mcp.CallToolResult, balanceOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, balanceOutput{}, err
	}
	balance, err := s.service.GetTeamBalance(ctx, input.Team)
	return nil, balanceOutput{Balance: balance}, err
}

func (s *Server) getTeamAnalytics(ctx context.Context, _ *mcp.CallToolRequest, input teamAnalyticsInput) (*mcp.CallToolResult, teamAnalyticsOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, teamAnalyticsOutput{}, err
	}
	start, end, err := parseAnalyticsRange(input.Start, input.End)
	if err != nil {
		return nil, teamAnalyticsOutput{}, err
	}
	balance, err := s.service.GetTeamBalance(ctx, input.Team)
	if err != nil {
		return nil, teamAnalyticsOutput{}, err
	}
	payment, err := s.service.GetPaymentMethod(ctx, input.Team)
	if err != nil {
		return nil, teamAnalyticsOutput{}, err
	}
	stats, err := s.service.GetTeamStats(ctx, input.Team, start, end)
	if err != nil {
		return nil, teamAnalyticsOutput{}, err
	}
	return nil, teamAnalyticsOutput{
		Team: input.Team, Start: start, End: end,
		Balance: balance, PaymentMethod: payment, Operators: stats,
	}, nil
}

func (s *Server) getGameAnalytics(ctx context.Context, _ *mcp.CallToolRequest, input gameAnalyticsInput) (*mcp.CallToolResult, gameAnalyticsOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, gameAnalyticsOutput{}, err
	}
	start, end, err := parseAnalyticsRange(input.Start, input.End)
	if err != nil {
		return nil, gameAnalyticsOutput{}, err
	}
	stats, err := s.service.GetGameStatsRange(ctx, input.Team, input.Game, start, end)
	if err != nil {
		return nil, gameAnalyticsOutput{}, err
	}
	graph, err := s.service.GetGameGraph(ctx, input.Team, input.Game, start, end)
	if err != nil {
		return nil, gameAnalyticsOutput{}, err
	}
	return nil, gameAnalyticsOutput{
		Team: input.Team, Game: input.Game, Start: start, End: end,
		Stats: stats, Graph: graph,
	}, nil
}

func parseAnalyticsRange(startRaw, endRaw string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	var err error
	if strings.TrimSpace(endRaw) != "" {
		end, err = time.Parse(time.RFC3339, endRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("end must be RFC3339: %w", err)
		}
	}
	start := end.Add(-30 * 24 * time.Hour)
	if strings.TrimSpace(startRaw) != "" {
		start, err = time.Parse(time.RFC3339, startRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("start must be RFC3339: %w", err)
		}
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("analytics start must be before end")
	}
	return start.UTC(), end.UTC(), nil
}

func (s *Server) listScratchFiles(ctx context.Context, _ *mcp.CallToolRequest, input scratchInput) (*mcp.CallToolResult, scratchOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, scratchOutput{}, err
	}
	files, err := s.service.ListScratch(ctx, input.Team, input.Game, input.Type)
	return nil, scratchOutput{Files: files}, err
}

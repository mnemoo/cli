package gamedetail

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/stake"
)

func TestApprovalShortcutOpensCurrentGameThread(t *testing.T) {
	model := Model{
		state: stateReady, teamSlug: "team", gameSlug: "game", gameName: "Game",
		detail: &stake.GameDetail{Approval: &stake.GameApproval{Open: true}},
	}
	_, command := model.handleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if command == nil {
		t.Fatal("approval shortcut returned no command")
	}
	message, ok := command().(ApprovalThreadRequestMsg)
	if !ok {
		t.Fatalf("approval shortcut returned %T", command())
	}
	if message.TeamSlug != "team" || message.GameSlug != "game" || message.GameName != "Game" {
		t.Fatalf("unexpected approval request: %#v", message)
	}
}

func TestPublisherProfitAndExpectedProfitUsePortalShares(t *testing.T) {
	actual := publisherProfit(5_000_000)
	expected := expectedProfit(4_000_000)
	if actual != 500_000 {
		t.Fatalf("publisherProfit() = %v, want 500000", actual)
	}
	if diff := expected - 300_000; diff < -0.001 || diff > 0.001 {
		t.Fatalf("expectedProfit() = %v, want 300000", expected)
	}
}

func TestStatsViewShowsProfitExpectedOnlineAndNoStanding(t *testing.T) {
	model := Model{
		state:  stateReady,
		detail: &stake.GameDetail{OnlinePlayers: 1_234},
		stats: &stake.GameStats{Stats: []stake.GameModeStat{{
			Mode: "base", Count: 100, Turnover: 100_000_000, Profit: 5_000_000,
			ExpectedReturn: 4_000_000, RTP: 0.95, EffectiveRTP: 0.95, NormalizedRTP: 0.95,
		}}},
		graphErr: errTestGraphUnavailable{},
	}
	var view strings.Builder
	model.viewStats(&view)
	for _, want := range []string{"Profit (10%)", "Expected (7.5%)", "Online now: 1,234", "$100.00", "$0.50", "$0.30"} {
		if !strings.Contains(view.String(), want) {
			t.Fatalf("stats view does not contain %q:\n%s", want, view.String())
		}
	}
	if strings.Contains(view.String(), "Standing") || strings.Contains(view.String(), "$0.20") {
		t.Fatalf("stats view still contains standing data:\n%s", view.String())
	}
}

type errTestGraphUnavailable struct{}

func (errTestGraphUnavailable) Error() string { return "graph unavailable" }

func TestStatsKeysPageDateWindowsAndCycleRange(t *testing.T) {
	model := Model{state: stateReady, activeTab: tabStats}

	older, command := model.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if command == nil || older.statsOffset != 1 || !older.graphLoading {
		t.Fatalf("left did not request older period: offset=%d loading=%v cmd=%v", older.statsOffset, older.graphLoading, command != nil)
	}

	older.graphLoading = false
	newer, command := older.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if command == nil || newer.statsOffset != 0 || !newer.graphLoading {
		t.Fatalf("right did not request newer period: offset=%d loading=%v cmd=%v", newer.statsOffset, newer.graphLoading, command != nil)
	}

	model.graphLoading = false
	cycled, command := model.handleKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if command == nil || cycled.statsRange != range7Days || !cycled.graphLoading {
		t.Fatalf("r did not cycle range: range=%d loading=%v cmd=%v", cycled.statsRange, cycled.graphLoading, command != nil)
	}
}

func TestAllTimeRangeDoesNotPage(t *testing.T) {
	model := Model{state: stateReady, activeTab: tabStats, statsRange: rangeAllTime}
	updated, command := model.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if command != nil || updated.statsOffset != 0 || updated.graphLoading {
		t.Fatalf("all-time left key paged range: offset=%d loading=%v cmd=%v", updated.statsOffset, updated.graphLoading, command != nil)
	}
}

func TestThirtyDayAndAllTimeWindows(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
	current := Model{statsRange: range30Days}
	start, end := current.graphWindow(now)
	if !start.Equal(now.Add(-30*24*time.Hour)) || !end.Equal(now) {
		t.Fatalf("current 30d window = %s..%s", start, end)
	}

	allTime := Model{statsRange: rangeAllTime, statsOffset: 99}
	start, end = allTime.graphWindow(now)
	if !start.Equal(time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(now) {
		t.Fatalf("all-time window = %s..%s", start, end)
	}
}

func TestFinancialSeriesUsesSharedProfitAndExpectedDefinitions(t *testing.T) {
	model := Model{
		stats: &stake.GameStats{Stats: []stake.GameModeStat{{Turnover: 100_000_000, ExpectedReturn: 4_000_000}}},
		graph: &stake.StatsGraph{Profit: []float64{5_000_000, 2_000_000}, Turnover: []float64{100_000_000, 100_000_000}},
	}
	profit, expected := model.financialSeries()
	if len(profit) != 2 || len(expected) != 2 {
		t.Fatalf("financial series lengths = %d/%d", len(profit), len(expected))
	}
	if profit[0] != 500_000 || expected[0] < 299_999.999 || expected[0] > 300_000.001 {
		t.Fatalf("first bucket = profit %v expected %v", profit[0], expected[0])
	}
	minimum, maximum := sharedSeriesBounds(profit, expected)
	lines := renderBrailleLine(profit, 12, 2, minimum, maximum)
	if len(lines) != 2 || strings.TrimSpace(strings.Join(lines, "")) == "" {
		t.Fatalf("braille chart = %#v, want a connected line", lines)
	}
}

func TestFormatPercentAcceptsFractionalAndPercentRTP(t *testing.T) {
	for _, test := range []struct {
		value float64
		want  string
	}{
		{value: 0.963, want: "96.30%"},
		{value: 1.06, want: "106.00%"},
		{value: 2.44, want: "244.00%"},
	} {
		if got := formatPercent(test.value); got != test.want {
			t.Fatalf("formatPercent(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestUncheckedMathPublishRequiresExtraConfirmation(t *testing.T) {
	model := Model{pubState: publishSelecting, pubCursor: 0}
	armed, command := model.handlePublishSelectKey("enter")
	if command != nil || armed.pubState != publishConfirmUnchecked || armed.pubType != "math" {
		t.Fatalf("math publish did not arm confirmation: command=%v state=%d type=%q", command != nil, armed.pubState, armed.pubType)
	}
	var view strings.Builder
	armed.viewPublishOverlay(&view)
	if !strings.Contains(view.String(), "Are you sure you want to publish anyway? [y/N]") {
		t.Fatalf("confirmation prompt missing: %s", view.String())
	}

	confirmed, command := armed.handlePublishSelectKey("y")
	if command == nil || confirmed.pubState != publishRunning {
		t.Fatalf("confirmed publish did not start: command=%v state=%d", command != nil, confirmed.pubState)
	}
}

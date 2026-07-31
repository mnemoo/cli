package gamedetail

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

type GoBackMsg struct{}

type UploadRequestMsg struct {
	TeamSlug string
	GameSlug string
}

type ApprovalThreadRequestMsg struct {
	TeamSlug string
	GameSlug string
	GameName string
}

type detailLoadedMsg struct {
	detail     *stake.GameDetail
	versions   []stake.GameVersion
	stats      *stake.GameStats
	graph      *stake.StatsGraph
	graphStart time.Time
	graphEnd   time.Time
	statsErr   error
	graphErr   error
}

type rangeLoadedMsg struct {
	stats    *stake.GameStats
	graph    *stake.StatsGraph
	start    time.Time
	end      time.Time
	statsErr error
	graphErr error
}

type detailErrorMsg struct {
	err error
}

type publishResultMsg struct {
	result *stake.PublishResult
	err    error
}

type publishPollMsg struct {
	versions []stake.GameVersion
	err      error
}

type publishPollTickMsg struct{}

type state int

const (
	stateLoading state = iota
	stateReady
	stateError
)

type publishState int

const (
	publishNone publishState = iota
	publishSelecting
	publishConfirmUnchecked
	publishRunning
	publishPolling
	publishDone
)

type tab int

const (
	tabInfo tab = iota
	tabStats
	tabVersions
	tabCount
)

func (t tab) label() string {
	switch t {
	case tabInfo:
		return "Info"
	case tabStats:
		return "Stats"
	case tabVersions:
		return "Versions"
	}
	return ""
}

type versionFilter int

const (
	filterAll versionFilter = iota
	filterMath
	filterFront
)

func (f versionFilter) label() string {
	switch f {
	case filterAll:
		return "All"
	case filterMath:
		return "Math"
	case filterFront:
		return "Front"
	}
	return ""
}

const versionsPerPage = 10

const (
	profitShareRate   = 0.10
	expectedShareRate = 0.075
)

type statsRange int

const (
	range24Hours statsRange = iota
	range7Days
	range14Days
	range30Days
	rangeAllTime
	rangeCount
)

func (r statsRange) label() string {
	switch r {
	case range24Hours:
		return "24h"
	case range7Days:
		return "7d"
	case range14Days:
		return "14d"
	case range30Days:
		return "30d"
	case rangeAllTime:
		return "all time"
	}
	return "24h"
}

func (r statsRange) duration() time.Duration {
	switch r {
	case range7Days:
		return 7 * 24 * time.Hour
	case range14Days:
		return 14 * 24 * time.Hour
	case range30Days:
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

var (
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	profitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	expectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

type Model struct {
	service      *service.Service
	teamSlug     string
	gameSlug     string
	gameName     string
	detail       *stake.GameDetail
	versions     []stake.GameVersion
	stats        *stake.GameStats
	graph        *stake.StatsGraph
	graphStart   time.Time
	graphEnd     time.Time
	statsErr     error
	graphErr     error
	graphLoading bool
	statsRange   statsRange
	statsOffset  int
	activeTab    tab
	verFilter    versionFilter
	verPage      int
	state        state
	spinner      spinner.Model
	err          error
	width        int
	height       int

	// Publish state
	pubState   publishState
	pubCursor  int // 0=math, 1=front
	pubType    string
	pubResult  *stake.PublishResult
	pubErr     error
	pubMsg     string
	pubPollN   int
	pubVersion int
}

func New(appService *service.Service, teamSlug, gameSlug, gameName string) Model {
	return Model{
		service:  appService,
		teamSlug: teamSlug,
		gameSlug: gameSlug,
		gameName: gameName,
		state:    stateLoading,
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadDetail())
}

func (m Model) loadDetail() tea.Cmd {
	appService := m.service
	teamSlug := m.teamSlug
	gameSlug := m.gameSlug
	start, end := m.graphWindow(time.Now())
	return func() tea.Msg {
		ctx := context.Background()
		var (
			snapshot      *service.GameSnapshot
			stats         *stake.GameStats
			graph         *stake.StatsGraph
			err, statsErr error
			graphErr      error
			wg            sync.WaitGroup
		)
		wg.Add(3)
		go func() {
			defer wg.Done()
			snapshot, err = appService.GetGameSnapshot(ctx, teamSlug, gameSlug)
		}()
		go func() {
			defer wg.Done()
			stats, statsErr = appService.GetGameStatsRange(ctx, teamSlug, gameSlug, start, end)
		}()
		go func() {
			defer wg.Done()
			graph, graphErr = appService.GetGameGraph(ctx, teamSlug, gameSlug, start, end)
		}()
		wg.Wait()
		if err != nil {
			return detailErrorMsg{err: err}
		}
		return detailLoadedMsg{
			detail: snapshot.Detail, versions: snapshot.Versions, stats: stats,
			graph: graph, graphStart: start, graphEnd: end, statsErr: statsErr, graphErr: graphErr,
		}
	}
}

func (m Model) loadRangeData() tea.Cmd {
	appService := m.service
	teamSlug, gameSlug := m.teamSlug, m.gameSlug
	start, end := m.graphWindow(time.Now())
	return func() tea.Msg {
		ctx := context.Background()
		var stats *stake.GameStats
		var graph *stake.StatsGraph
		var statsErr, graphErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			stats, statsErr = appService.GetGameStatsRange(ctx, teamSlug, gameSlug, start, end)
		}()
		go func() {
			defer wg.Done()
			graph, graphErr = appService.GetGameGraph(ctx, teamSlug, gameSlug, start, end)
		}()
		wg.Wait()
		return rangeLoadedMsg{stats: stats, graph: graph, start: start, end: end, statsErr: statsErr, graphErr: graphErr}
	}
}

func (m Model) graphWindow(now time.Time) (time.Time, time.Time) {
	if m.statsRange == rangeAllTime {
		return time.Date(2025, time.January, 1, 0, 0, 0, 0, now.Location()), now
	}

	duration := m.statsRange.duration()
	end := now.Add(-time.Duration(m.statsOffset) * duration)
	return end.Add(-duration), end
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case detailLoadedMsg:
		m.detail = msg.detail
		m.versions = msg.versions
		m.stats = msg.stats
		m.graph = msg.graph
		m.graphStart = msg.graphStart
		m.graphEnd = msg.graphEnd
		m.statsErr = msg.statsErr
		m.graphErr = msg.graphErr
		m.graphLoading = false
		m.state = stateReady
		return m, nil

	case rangeLoadedMsg:
		m.stats = msg.stats
		m.graph = msg.graph
		m.graphStart = msg.start
		m.graphEnd = msg.end
		m.statsErr = msg.statsErr
		m.graphErr = msg.graphErr
		m.graphLoading = false
		return m, nil

	case detailErrorMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case publishResultMsg:
		return m.handlePublishResult(msg)

	case publishPollTickMsg:
		return m, m.pollVersions()

	case publishPollMsg:
		return m.handlePublishPoll(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		if m.state == stateLoading || m.graphLoading || m.pubState == publishRunning || m.pubState == publishPolling {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()

	// During publish selecting, intercept keys
	if m.pubState == publishSelecting || m.pubState == publishConfirmUnchecked {
		return m.handlePublishSelectKey(key)
	}

	// During publish done, any key clears the message
	if m.pubState == publishDone {
		m.pubState = publishNone
		m.pubMsg = ""
		m.pubErr = nil
		return m, nil
	}

	// During publish running/polling, only allow esc to cancel view
	if m.pubState == publishRunning || m.pubState == publishPolling {
		return m, nil
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		return m, func() tea.Msg { return GoBackMsg{} }
	case "u":
		if m.state == stateReady {
			teamSlug, gameSlug := m.teamSlug, m.gameSlug
			return m, func() tea.Msg { return UploadRequestMsg{TeamSlug: teamSlug, GameSlug: gameSlug} }
		}
	case "a":
		if m.state == stateReady && m.detail != nil && m.detail.Approval != nil {
			teamSlug, gameSlug, gameName := m.teamSlug, m.gameSlug, m.gameName
			return m, func() tea.Msg {
				return ApprovalThreadRequestMsg{TeamSlug: teamSlug, GameSlug: gameSlug, GameName: gameName}
			}
		}
	case "p":
		if m.state == stateReady {
			m.pubState = publishSelecting
			m.pubCursor = 0
			return m, nil
		}
	case "tab":
		m.activeTab = (m.activeTab + 1) % tabCount
		return m, nil
	case "shift+tab":
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return m, nil
	}

	if m.state == stateError && key == "r" {
		m.state = stateLoading
		return m, tea.Batch(m.spinner.Tick, m.loadDetail())
	}

	if m.activeTab == tabStats && m.state == stateReady {
		switch key {
		case "left", "h":
			if !m.graphLoading && m.statsRange != rangeAllTime {
				m.statsOffset++
				m.graphLoading = true
				return m, tea.Batch(m.spinner.Tick, m.loadRangeData())
			}
		case "right", "l":
			if !m.graphLoading && m.statsRange != rangeAllTime && m.statsOffset > 0 {
				m.statsOffset--
				m.graphLoading = true
				return m, tea.Batch(m.spinner.Tick, m.loadRangeData())
			}
		case "r":
			if !m.graphLoading {
				m.statsRange = (m.statsRange + 1) % rangeCount
				m.statsOffset = 0
				m.graphLoading = true
				return m, tea.Batch(m.spinner.Tick, m.loadRangeData())
			}
		case "0", "home":
			if !m.graphLoading && m.statsOffset != 0 {
				m.statsOffset = 0
				m.graphLoading = true
				return m, tea.Batch(m.spinner.Tick, m.loadRangeData())
			}
		}
	}

	if m.activeTab == tabVersions && m.state == stateReady {
		filtered := m.filteredVersions()
		maxPage := m.maxPage(filtered)
		switch key {
		case "f":
			m.verFilter = (m.verFilter + 1) % 3
			m.verPage = 0
			return m, nil
		case "left", "h":
			if m.verPage > 0 {
				m.verPage--
			}
		case "right", "l":
			if m.verPage < maxPage {
				m.verPage++
			}
		}
	}

	return m, nil
}

func (m Model) handlePublishSelectKey(key string) (Model, tea.Cmd) {
	if m.pubState == publishConfirmUnchecked {
		switch key {
		case "y", "enter":
			m.pubState = publishRunning
			m.pubMsg = ""
			m.pubErr = nil
			return m, tea.Batch(m.spinner.Tick, m.doPublish())
		case "n", "q", "esc":
			m.pubState = publishSelecting
			return m, nil
		}
		return m, nil
	}
	switch key {
	case "esc", "q":
		m.pubState = publishNone
		return m, nil
	case "up", "k":
		if m.pubCursor > 0 {
			m.pubCursor--
		}
		return m, nil
	case "down", "j":
		if m.pubCursor < 1 {
			m.pubCursor++
		}
		return m, nil
	case "enter":
		if m.pubCursor == 0 {
			m.pubType = "math"
			m.pubState = publishConfirmUnchecked
			return m, nil
		} else {
			m.pubType = "front"
		}
		m.pubState = publishRunning
		m.pubMsg = ""
		m.pubErr = nil
		return m, tea.Batch(m.spinner.Tick, m.doPublish())
	}
	return m, nil
}

func (m Model) doPublish() tea.Cmd {
	appService := m.service
	team, game, kind := m.teamSlug, m.gameSlug, m.pubType
	return func() tea.Msg {
		var result *stake.PublishResult
		var err error
		if kind == "math" {
			result, err = appService.PublishMathUnchecked(context.Background(), team, game)
		} else {
			result, err = appService.Publish(context.Background(), team, game, kind)
		}
		return publishResultMsg{result: result, err: err}
	}
}

func (m Model) handlePublishResult(msg publishResultMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.pubState = publishDone
		m.pubErr = msg.err
		m.pubMsg = fmt.Sprintf("Publish %s failed: %v", m.pubType, msg.err)
		return m, nil
	}

	if msg.result.IsError() {
		m.pubState = publishDone
		m.pubErr = fmt.Errorf("%s", msg.result.Message)
		m.pubMsg = fmt.Sprintf("Publish %s error [%s]:\n  %s", m.pubType, msg.result.Code, msg.result.Message)
		if msg.result.File != nil {
			m.pubMsg += fmt.Sprintf("\n  File: %s", *msg.result.File)
		}
		if msg.result.Mode != nil {
			m.pubMsg += fmt.Sprintf("\n  Mode: %s", *msg.result.Mode)
		}
		return m, nil
	}

	m.pubResult = msg.result
	m.pubVersion = msg.result.Version
	m.pubState = publishPolling
	m.pubPollN = 0
	m.pubMsg = fmt.Sprintf("Published %s v%d (changed: %v). Refreshing versions...", m.pubType, msg.result.Version, msg.result.Changed)
	return m, tea.Batch(m.spinner.Tick, m.pollAfterDelay())
}

func (m Model) pollAfterDelay() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return publishPollTickMsg{}
	})
}

func (m Model) pollVersions() tea.Cmd {
	appService := m.service
	team, game := m.teamSlug, m.gameSlug
	return func() tea.Msg {
		versions, err := appService.ListGameVersions(context.Background(), team, game)
		return publishPollMsg{versions: versions, err: err}
	}
}

func (m Model) handlePublishPoll(msg publishPollMsg) (Model, tea.Cmd) {
	m.pubPollN++

	if msg.err != nil && m.pubPollN < 10 {
		return m, m.pollAfterDelay()
	}

	if msg.err != nil {
		m.pubState = publishDone
		m.pubMsg = fmt.Sprintf("Published %s v%d but failed to refresh versions: %v", m.pubType, m.pubVersion, msg.err)
		return m, nil
	}

	sort.Slice(msg.versions, func(i, j int) bool {
		return msg.versions[i].Created > msg.versions[j].Created
	})

	found := false
	for _, v := range msg.versions {
		if v.Type == m.pubType && v.Version == m.pubVersion {
			found = true
			break
		}
	}

	if !found && m.pubPollN < 10 {
		return m, m.pollAfterDelay()
	}

	m.versions = msg.versions
	m.pubState = publishDone
	if found {
		m.pubMsg = fmt.Sprintf("Published %s v%d successfully!", m.pubType, m.pubVersion)
	} else {
		m.pubMsg = fmt.Sprintf("Published %s v%d — version not yet visible (may take a moment)", m.pubType, m.pubVersion)
	}
	return m, nil
}

func (m Model) filteredVersions() []stake.GameVersion {
	if m.verFilter == filterAll {
		return m.versions
	}
	kind := "math"
	if m.verFilter == filterFront {
		kind = "front"
	}
	var out []stake.GameVersion
	for _, v := range m.versions {
		if v.Type == kind {
			out = append(out, v)
		}
	}
	return out
}

func (m Model) maxPage(filtered []stake.GameVersion) int {
	if len(filtered) == 0 {
		return 0
	}
	return (len(filtered) - 1) / versionsPerPage
}

func (m Model) View() string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n  Game — %s\n\n", m.gameName)

	switch m.state {
	case stateLoading:
		fmt.Fprintf(&b, "  %s Loading...\n", m.spinner.View())

	case stateReady:
		b.WriteString(m.viewTabs())
		b.WriteString("\n")

		switch m.activeTab {
		case tabInfo:
			m.viewInfo(&b)
		case tabStats:
			m.viewStats(&b)
		case tabVersions:
			m.viewVersions(&b)
		}

		m.viewPublishOverlay(&b)

		if m.detail != nil && m.detail.Approval != nil {
			b.WriteString("\n  Tab: next tab • a: approval thread • u: upload • p: publish • Esc: back\n")
		} else {
			b.WriteString("\n  Tab: next tab • u: upload • p: publish • Esc: back\n")
		}

	case stateError:
		fmt.Fprintf(&b, "  Error: %s\n\n", m.err.Error())
		b.WriteString("  r: retry • Esc: back\n")
	}

	return b.String()
}

func (m Model) viewPublishOverlay(b *strings.Builder) {
	switch m.pubState {
	case publishSelecting:
		b.WriteString("\n  ─── Publish ───\n\n")
		opts := []string{"Math", "Front-end"}
		for i, opt := range opts {
			cursor := "  "
			if i == m.pubCursor {
				cursor = "> "
			}
			fmt.Fprintf(b, "  %s%s\n", cursor, opt)
		}
		b.WriteString("\n  j/k: navigate • Enter: publish • Esc: cancel\n")
	case publishConfirmUnchecked:
		b.WriteString("\n  ─── Compliance warning ───\n\n")
		b.WriteString("  Math compliance cannot be checked without a local bundle.\n")
		b.WriteString("  Are you sure you want to publish anyway? [y/N]\n")
	case publishRunning:
		fmt.Fprintf(b, "\n  %s Publishing %s...\n", m.spinner.View(), m.pubType)
	case publishPolling:
		fmt.Fprintf(b, "\n  %s %s\n", m.spinner.View(), m.pubMsg)
	case publishDone:
		icon := "✓"
		if m.pubErr != nil {
			icon = "✗"
		}
		maxW := m.width - 6
		if maxW < 40 {
			maxW = 40
		}
		lines := strings.Split(m.pubMsg, "\n")
		first := wrapLine(lines[0], maxW)
		fmt.Fprintf(b, "\n  %s %s\n", icon, first[0])
		for _, w := range first[1:] {
			fmt.Fprintf(b, "    %s\n", w)
		}
		for _, line := range lines[1:] {
			for _, wrapped := range wrapLine(line, maxW) {
				fmt.Fprintf(b, "    %s\n", wrapped)
			}
		}
		b.WriteString("  Press any key to dismiss.\n")
	}
}

func wrapLine(s string, maxWidth int) []string {
	if len(s) <= maxWidth {
		return []string{s}
	}
	var lines []string
	for len(s) > maxWidth {
		cut := maxWidth
		for cut > 0 && s[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = maxWidth
		}
		lines = append(lines, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if len(s) > 0 {
		lines = append(lines, s)
	}
	return lines
}

func (m Model) viewTabs() string {
	var parts []string
	for i := tab(0); i < tabCount; i++ {
		label := i.label()
		if i == m.activeTab {
			parts = append(parts, fmt.Sprintf("[%s]", label))
		} else {
			parts = append(parts, fmt.Sprintf(" %s ", label))
		}
	}
	return "  " + strings.Join(parts, "  ") + "\n"
}

func (m Model) viewInfo(b *strings.Builder) {
	d := m.detail
	fmt.Fprintf(b, "  Name:     %s\n", d.Name)
	fmt.Fprintf(b, "  Slug:     %s\n", d.Slug)
	fmt.Fprintf(b, "  Rating:   %s\n", ratingStars(d.Rating))
	fmt.Fprintf(b, "  Online:   %d\n", d.OnlinePlayers)

	if d.Approval != nil {
		status := "closed"
		if d.Approval.Open {
			status = "open"
		}
		if d.Approval.Locked {
			status += ", locked"
		}
		fmt.Fprintf(b, "  Approval: %s (%s)\n", d.Approval.Column, status)
	} else {
		b.WriteString("  Approval: —\n")
	}

	mathVer, frontVer := m.activeVersions()
	b.WriteString("\n  Active Versions\n")
	fmt.Fprintf(b, "  Math:  %s\n", activeStyle.Render(mathVer))
	fmt.Fprintf(b, "  Front: %s\n", activeStyle.Render(frontVer))
}

func (m Model) activeVersions() (string, string) {
	mathVer := "—"
	frontVer := "—"
	for _, v := range m.versions {
		for _, a := range v.Approved {
			if a.Active {
				label := fmt.Sprintf("v%d (%s)", v.Version, a.Slug)
				switch v.Type {
				case "math":
					if mathVer == "—" {
						mathVer = label
					} else {
						mathVer += ", " + label
					}
				case "front":
					if frontVer == "—" {
						frontVer = label
					} else {
						frontVer += ", " + label
					}
				}
			}
		}
	}
	return mathVer, frontVer
}

func (m Model) viewStats(b *strings.Builder) {
	if m.graphLoading {
		fmt.Fprintf(b, "  %s Loading %s stats and graph...\n", m.spinner.View(), m.statsRange.label())
		return
	}

	start := m.graphStart.Format("2006-01-02 15:04 MST")
	end := m.graphEnd.Format("2006-01-02 15:04 MST")
	online := "—"
	if m.detail != nil {
		online = formatNumber(int64(m.detail.OnlinePlayers))
	}
	fmt.Fprintf(b, "  Range: %s  •  %s → %s  •  Online now: %s\n\n", m.statsRange.label(), start, end, online)
	if m.statsErr != nil {
		fmt.Fprintf(b, "  Stats unavailable for this range: %v\n", m.statsErr)
		return
	}
	if m.stats == nil || len(m.stats.Stats) == 0 {
		b.WriteString("  No stats available.\n")
		return
	}

	var totalProfit, totalExpected float64
	for _, stat := range m.stats.Stats {
		totalProfit += publisherProfit(stat.Profit)
		totalExpected += expectedProfit(stat.ExpectedReturn)
	}
	fmt.Fprintf(b, "  Mode totals: %s  •  %s\n\n",
		profitStyle.Render("Profit (10%) "+formatMoney(totalProfit)),
		expectedStyle.Render("Expected (7.5%) "+formatMoney(totalExpected)))

	m.viewFinancialChart(b)
	b.WriteString("\n")

	fmt.Fprintf(b, "  %-14s %12s %14s %16s %16s %8s %8s %8s\n",
		"Mode", "Bets", "Turnover", "Profit (10%)", "Expected (7.5%)", "RTP", "Eff.RTP", "Nrm.RTP")
	b.WriteString("  " + strings.Repeat("─", 105) + "\n")

	for _, s := range m.stats.Stats {
		mode := s.Mode
		if len(mode) > 12 {
			mode = mode[:12] + "…"
		}
		bets := formatNumber(s.Count)
		turnover := formatMoney(s.Turnover)
		profitValue := publisherProfit(s.Profit)
		expectedValue := expectedProfit(s.ExpectedReturn)
		profit := profitStyle.Render(fmt.Sprintf("%16s", formatMoney(profitValue)))
		expected := expectedStyle.Render(fmt.Sprintf("%16s", formatMoney(expectedValue)))
		rtp := formatPercent(s.RTP)
		effRTP := formatPercent(s.EffectiveRTP)
		nrmRTP := formatPercent(s.NormalizedRTP)

		fmt.Fprintf(b, "  %-14s %12s %14s %s %s %8s %8s %8s\n",
			mode, bets, turnover, profit, expected, rtp, effRTP, nrmRTP)
	}

}

func (m Model) viewFinancialChart(b *strings.Builder) {
	label := m.statsRange.label()
	if m.graphErr != nil {
		fmt.Fprintf(b, "  Profit / Expected graph unavailable: %v\n", m.graphErr)
		fmt.Fprintf(b, "  Range: %s  •  ←/→ dates  •  r range  •  0 current\n", label)
		return
	}

	profits, expected := m.financialSeries()
	if len(profits) == 0 || len(expected) == 0 {
		b.WriteString("  No graph data available for this period.\n")
		fmt.Fprintf(b, "  Range: %s  •  ←/→ dates  •  r range  •  0 current\n", label)
		return
	}

	width := m.width - 8
	if width < 24 {
		width = 24
	}
	if width > 64 {
		width = 64
	}
	profits = resampleSums(profits, width*2)
	expected = resampleSums(expected, width*2)
	minimum, maximum := sharedSeriesBounds(profits, expected)

	b.WriteString("  Profit / Expected — shared scale\n")
	b.WriteString("  Profit (10%)\n")
	for _, line := range renderBrailleLine(profits, width, 2, minimum, maximum) {
		fmt.Fprintf(b, "  %s\n", profitStyle.Render(line))
	}
	b.WriteString("  Expected (7.5%)\n")
	for _, line := range renderBrailleLine(expected, width, 2, minimum, maximum) {
		fmt.Fprintf(b, "  %s\n", expectedStyle.Render(line))
	}
	fmt.Fprintf(b, "  Scale per bucket: %s → %s\n", formatMoney(minimum), formatMoney(maximum))
	if m.statsRange == rangeAllTime {
		b.WriteString("  r: range  •  all-time period cannot be paged\n")
	} else {
		fmt.Fprintf(b, "  ←/→ dates  •  r range  •  0 current%s\n", olderPeriodLabel(m.statsOffset))
	}
}

func (m Model) financialSeries() ([]float64, []float64) {
	if m.graph == nil {
		return nil, nil
	}
	expectedRate, ok := m.expectedPublisherRate()
	if !ok {
		return nil, nil
	}
	count := minInt(len(m.graph.Profit), len(m.graph.Turnover))
	profits := make([]float64, count)
	expected := make([]float64, count)
	for i := 0; i < count; i++ {
		profits[i] = publisherProfit(m.graph.Profit[i])
		expected[i] = m.graph.Turnover[i] * expectedRate
	}
	return profits, expected
}

func (m Model) expectedPublisherRate() (float64, bool) {
	if m.stats == nil {
		return 0, false
	}
	var turnover, expected float64
	for _, stat := range m.stats.Stats {
		if stat.Turnover <= 0 || stat.ExpectedReturn < 0 {
			continue
		}
		turnover += stat.Turnover
		expected += expectedProfit(stat.ExpectedReturn)
	}
	if turnover == 0 {
		return 0, false
	}
	return expected / turnover, true
}

func publisherProfit(ggr float64) float64 {
	return ggr * profitShareRate
}

func expectedProfit(expectedReturn float64) float64 {
	return expectedReturn * expectedShareRate
}

func formatPercent(value float64) string {
	if value <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", value*100)
}

func sharedSeriesBounds(series ...[]float64) (float64, float64) {
	var minimum, maximum float64
	found := false
	for _, values := range series {
		for _, value := range values {
			if !found || value < minimum {
				minimum = value
			}
			if !found || value > maximum {
				maximum = value
			}
			found = true
		}
	}
	return minimum, maximum
}

func renderBrailleLine(values []float64, width, height int, minimum, maximum float64) []string {
	if len(values) == 0 || width <= 0 || height <= 0 {
		return nil
	}
	pixelWidth, pixelHeight := width*2, height*4
	canvas := make([][]byte, height)
	for row := range canvas {
		canvas[row] = make([]byte, width)
	}

	point := func(index int) (int, int) {
		x := 0
		if len(values) > 1 {
			x = int(math.Round(float64(index) * float64(pixelWidth-1) / float64(len(values)-1)))
		}
		normalized := 0.5
		if maximum > minimum {
			normalized = (values[index] - minimum) / (maximum - minimum)
		}
		if normalized < 0 {
			normalized = 0
		} else if normalized > 1 {
			normalized = 1
		}
		y := int(math.Round((1 - normalized) * float64(pixelHeight-1)))
		return x, y
	}

	previousX, previousY := point(0)
	setBraillePixel(canvas, previousX, previousY)
	for i := 1; i < len(values); i++ {
		x, y := point(i)
		drawBrailleSegment(canvas, previousX, previousY, x, y)
		previousX, previousY = x, y
	}

	lines := make([]string, height)
	for row := range canvas {
		var line strings.Builder
		for _, dots := range canvas[row] {
			if dots == 0 {
				line.WriteByte(' ')
			} else {
				line.WriteRune(rune(0x2800 + int(dots)))
			}
		}
		lines[row] = strings.TrimRight(line.String(), " ")
	}
	return lines
}

func drawBrailleSegment(canvas [][]byte, x0, y0, x1, y1 int) {
	dx := absInt(x1 - x0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	dy := -absInt(y1 - y0)
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		setBraillePixel(canvas, x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		twiceErr := 2 * err
		if twiceErr >= dy {
			err += dy
			x0 += sx
		}
		if twiceErr <= dx {
			err += dx
			y0 += sy
		}
	}
}

func setBraillePixel(canvas [][]byte, x, y int) {
	if len(canvas) == 0 || x < 0 || y < 0 || y >= len(canvas)*4 || x >= len(canvas[0])*2 {
		return
	}
	dots := [4][2]byte{{1 << 0, 1 << 3}, {1 << 1, 1 << 4}, {1 << 2, 1 << 5}, {1 << 6, 1 << 7}}
	canvas[y/4][x/2] |= dots[y%4][x%2]
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func resampleSums(values []float64, width int) []float64 {
	if width <= 0 || len(values) <= width {
		return append([]float64(nil), values...)
	}
	result := make([]float64, width)
	for i := range result {
		start := i * len(values) / width
		end := (i + 1) * len(values) / width
		for _, value := range values[start:end] {
			result[i] += value
		}
	}
	return result
}

func olderPeriodLabel(offset int) string {
	if offset == 0 {
		return ""
	}
	return fmt.Sprintf("  •  %d period(s) back", offset)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Model) viewVersions(b *strings.Builder) {
	filtered := m.filteredVersions()

	fmt.Fprintf(b, "  Filter: %s   ", m.filterTabs())
	fmt.Fprintf(b, "(%d versions)\n\n", len(filtered))

	if len(filtered) == 0 {
		b.WriteString("  No versions found.\n")
		return
	}

	maxPage := m.maxPage(filtered)
	start := m.verPage * versionsPerPage
	end := start + versionsPerPage
	if end > len(filtered) {
		end = len(filtered)
	}
	page := filtered[start:end]

	fmt.Fprintf(b, "  %-8s %-8s %-22s %s\n", "Type", "Ver", "Created", "Operators")
	b.WriteString("  " + strings.Repeat("─", 70) + "\n")

	for _, v := range page {
		created := time.Unix(int64(v.Created)/1000, 0).Format("2006-01-02 15:04")
		ops := operatorsList(v.Approved)
		hasActive := isActive(v)

		line := fmt.Sprintf("  %-8s v%-7d %-22s %s", v.Type, v.Version, created, ops)
		if hasActive {
			b.WriteString(activeStyle.Render(line) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}

	fmt.Fprintf(b, "\n  Page %d/%d  •  ←/→: page  •  f: filter\n", m.verPage+1, maxPage+1)
}

func (m Model) filterTabs() string {
	filters := []versionFilter{filterAll, filterMath, filterFront}
	var parts []string
	for _, f := range filters {
		if f == m.verFilter {
			parts = append(parts, fmt.Sprintf("[%s]", f.label()))
		} else {
			parts = append(parts, f.label())
		}
	}
	return strings.Join(parts, " / ")
}

func isActive(v stake.GameVersion) bool {
	for _, a := range v.Approved {
		if a.Active {
			return true
		}
	}
	return false
}

func ratingStars(rating *float64) string {
	if rating == nil {
		return "—"
	}
	r := *rating
	var stars int
	switch {
	case r >= 90:
		stars = 3
	case r >= 60:
		stars = 2
	case r >= 30:
		stars = 1
	default:
		return "☆☆☆"
	}
	return strings.Repeat("★", stars) + strings.Repeat("☆", 3-stars)
}

func operatorsList(approved []stake.VersionApproval) string {
	if len(approved) == 0 {
		return "—"
	}
	var parts []string
	for _, a := range approved {
		label := a.Slug
		if a.Active {
			label += "*"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func formatMoney(raw float64) string {
	val := raw / 1e6
	neg := val < 0
	if neg {
		val = -val
	}
	whole := int64(val)
	frac := int64((val-float64(whole))*100 + 0.5)
	if frac >= 100 {
		whole++
		frac -= 100
	}
	prefix := "$"
	if neg {
		prefix = "-$"
	}
	return fmt.Sprintf("%s%s.%02d", prefix, formatNumber(whole), frac)
}

func formatNumber(n int64) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

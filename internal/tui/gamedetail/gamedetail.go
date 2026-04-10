package gamedetail

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mnemoo/cli/internal/api"
)

type GoBackMsg struct{}

type UploadRequestMsg struct {
	TeamSlug string
	GameSlug string
}

type detailLoadedMsg struct {
	detail   *api.TeamGameDetail
	versions []api.GameVersionHistoryItem
	stats    *api.GameStatsByModeResponse
}

type detailErrorMsg struct {
	err error
}

type publishResultMsg struct {
	result *api.PublishResult
	err    error
}

type publishPollMsg struct {
	versions []api.GameVersionHistoryItem
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

var (
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

type Model struct {
	client    *api.Client
	teamSlug  string
	gameSlug  string
	gameName  string
	detail    *api.TeamGameDetail
	versions  []api.GameVersionHistoryItem
	stats     *api.GameStatsByModeResponse
	activeTab tab
	verFilter versionFilter
	verPage   int
	state     state
	spinner   spinner.Model
	err       error
	width     int
	height    int

	// Publish state
	pubState   publishState
	pubCursor  int // 0=math, 1=front
	pubType    string
	pubResult  *api.PublishResult
	pubErr     error
	pubMsg     string
	pubPollN   int
	pubVersion int
}

func New(client *api.Client, teamSlug, gameSlug, gameName string) Model {
	return Model{
		client:   client,
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
	client := m.client
	teamSlug := m.teamSlug
	gameSlug := m.gameSlug
	return func() tea.Msg {
		ctx := context.Background()
		detail, err := client.GetGameDetail(ctx, teamSlug, gameSlug)
		if err != nil {
			return detailErrorMsg{err: err}
		}
		versions, err := client.GetGameVersions(ctx, teamSlug, gameSlug)
		if err != nil {
			return detailErrorMsg{err: err}
		}
		stats, _ := client.GetGameStats(ctx, teamSlug, gameSlug)
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].Created > versions[j].Created
		})
		return detailLoadedMsg{detail: detail, versions: versions, stats: stats}
	}
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
		m.state = stateReady
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
		if m.state == stateLoading || m.pubState == publishRunning || m.pubState == publishPolling {
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
	if m.pubState == publishSelecting {
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
	client := m.client
	team, game, kind := m.teamSlug, m.gameSlug, m.pubType
	return func() tea.Msg {
		result, err := client.Publish(context.Background(), team, game, kind)
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
	client := m.client
	team, game := m.teamSlug, m.gameSlug
	return func() tea.Msg {
		versions, err := client.GetGameVersions(context.Background(), team, game)
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

func (m Model) filteredVersions() []api.GameVersionHistoryItem {
	if m.verFilter == filterAll {
		return m.versions
	}
	kind := "math"
	if m.verFilter == filterFront {
		kind = "front"
	}
	var out []api.GameVersionHistoryItem
	for _, v := range m.versions {
		if v.Type == kind {
			out = append(out, v)
		}
	}
	return out
}

func (m Model) maxPage(filtered []api.GameVersionHistoryItem) int {
	if len(filtered) == 0 {
		return 0
	}
	return (len(filtered) - 1) / versionsPerPage
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("\n  Game — %s\n\n", m.gameName))

	switch m.state {
	case stateLoading:
		b.WriteString(fmt.Sprintf("  %s Loading...\n", m.spinner.View()))

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

		b.WriteString("\n  Tab: next tab • u: upload • p: publish • Esc: back\n")

	case stateError:
		b.WriteString(fmt.Sprintf("  Error: %s\n\n", m.err.Error()))
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
			b.WriteString(fmt.Sprintf("  %s%s\n", cursor, opt))
		}
		b.WriteString("\n  j/k: navigate • Enter: publish • Esc: cancel\n")
	case publishRunning:
		b.WriteString(fmt.Sprintf("\n  %s Publishing %s...\n", m.spinner.View(), m.pubType))
	case publishPolling:
		b.WriteString(fmt.Sprintf("\n  %s %s\n", m.spinner.View(), m.pubMsg))
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
		b.WriteString(fmt.Sprintf("\n  %s %s\n", icon, first[0]))
		for _, w := range first[1:] {
			b.WriteString(fmt.Sprintf("    %s\n", w))
		}
		for _, line := range lines[1:] {
			for _, wrapped := range wrapLine(line, maxW) {
				b.WriteString(fmt.Sprintf("    %s\n", wrapped))
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
	b.WriteString(fmt.Sprintf("  Name:     %s\n", d.Name))
	b.WriteString(fmt.Sprintf("  Slug:     %s\n", d.Slug))
	b.WriteString(fmt.Sprintf("  Rating:   %s\n", ratingStars(d.Rating)))
	b.WriteString(fmt.Sprintf("  Online:   %d\n", d.OnlinePlayers))

	if d.Approval != nil {
		status := "closed"
		if d.Approval.Open {
			status = "open"
		}
		if d.Approval.Locked {
			status += ", locked"
		}
		b.WriteString(fmt.Sprintf("  Approval: %s (%s)\n", d.Approval.Column, status))
	} else {
		b.WriteString("  Approval: —\n")
	}

	mathVer, frontVer := m.activeVersions()
	b.WriteString("\n  Active Versions\n")
	b.WriteString(fmt.Sprintf("  Math:  %s\n", activeStyle.Render(mathVer)))
	b.WriteString(fmt.Sprintf("  Front: %s\n", activeStyle.Render(frontVer)))
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
	if m.stats == nil || len(m.stats.Stats) == 0 {
		b.WriteString("  No stats available.\n")
		return
	}

	b.WriteString(fmt.Sprintf("  %-14s %12s %14s %14s %8s %8s %8s\n",
		"Mode", "Bets", "Turnover", "Profit", "RTP", "Eff.RTP", "Nrm.RTP"))
	b.WriteString("  " + strings.Repeat("─", 82) + "\n")

	for _, s := range m.stats.Stats {
		mode := s.Mode
		if len(mode) > 12 {
			mode = mode[:12] + "…"
		}
		bets := formatNumber(s.Count)
		turnover := formatMoney(s.Turnover)
		profit := formatMoney(s.Profit)
		rtp := fmt.Sprintf("%.2f%%", s.Rtp*100)
		effRtp := fmt.Sprintf("%.2f%%", s.EffectiveRtp*100)
		nrmRtp := fmt.Sprintf("%.2f%%", s.NormalizedRtp*100)

		b.WriteString(fmt.Sprintf("  %-14s %12s %14s %14s %8s %8s %8s\n",
			mode, bets, turnover, profit, rtp, effRtp, nrmRtp))
	}
}

func (m Model) viewVersions(b *strings.Builder) {
	filtered := m.filteredVersions()

	b.WriteString(fmt.Sprintf("  Filter: %s   ", m.filterTabs()))
	b.WriteString(fmt.Sprintf("(%d versions)\n\n", len(filtered)))

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

	b.WriteString(fmt.Sprintf("  %-8s %-8s %-22s %s\n", "Type", "Ver", "Created", "Operators"))
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

	b.WriteString(fmt.Sprintf("\n  Page %d/%d  •  ←/→: page  •  f: filter\n", m.verPage+1, maxPage+1))
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

func isActive(v api.GameVersionHistoryItem) bool {
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

func operatorsList(approved []api.VersionApproved) string {
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
	val := raw / 1e7
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

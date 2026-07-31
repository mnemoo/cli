package conversations

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	conversationcore "github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

const (
	listRefreshInterval   = 10 * time.Second
	threadRefreshInterval = 5 * time.Second
)

type GoBackMsg struct{}

type screen int

const (
	screenTeams screen = iota
	screenThreads
	screenThread
)

type teamsLoadedMsg struct {
	teams []stake.Team
	err   error
}

type threadsLoadedMsg struct {
	team   string
	result conversationcore.RefreshResult
	err    error
}

type threadLoadedMsg struct {
	team   string
	game   string
	thread *conversationcore.ThreadView
	err    error
}

type messageSentMsg struct {
	team   string
	game   string
	thread *conversationcore.ThreadView
	err    error
}

type readChangedMsg struct {
	read  bool
	stats stake.MessageStats
	err   error
}

type refreshTickMsg struct {
	generation int64
}

type Model struct {
	service           *service.Service
	manager           *conversationcore.Manager
	screen            screen
	teams             []stake.Team
	threads           []conversationcore.ThreadSummary
	thread            *conversationcore.ThreadView
	teamCursor        int
	threadCursor      int
	selectedTeam      *stake.Team
	selectedGame      string
	loading           bool
	refreshing        bool
	sending           bool
	unreadOnly        bool
	composer          bool
	private           bool
	searching         bool
	directThread      bool
	spinner           spinner.Model
	textarea          textarea.Model
	search            textinput.Model
	viewport          viewport.Model
	width             int
	height            int
	err               error
	status            string
	lastRefresh       time.Time
	refreshGeneration int64
	refreshScheduled  bool
	timelineBlocks    []timelineBlock
	timelineOffsets   []int
	timelineCursor    int
	expandedGroups    map[string]bool
}

func New(appService *service.Service, manager *conversationcore.Manager, width, height int) Model {
	area := textarea.New()
	area.Placeholder = "Reply to reviewer…"
	area.SetWidth(max(30, width-8))
	area.SetHeight(5)
	search := textinput.New()
	search.Placeholder = "Filter games…"
	search.CharLimit = 80
	search.SetWidth(max(20, min(50, width-18)))
	view := viewport.New(viewport.WithWidth(max(30, width-4)), viewport.WithHeight(max(8, height-9)))
	return Model{
		service: appService, manager: manager, screen: screenTeams, loading: true,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)), textarea: area, search: search,
		viewport: view, width: width, height: height,
		refreshGeneration: time.Now().UnixNano(), expandedGroups: make(map[string]bool),
	}
}

// NewThread opens one game's approval conversation directly. It avoids the
// team-wide conversation scan used by the Messages browser.
func NewThread(
	appService *service.Service,
	manager *conversationcore.Manager,
	team stake.Team,
	gameSlug string,
	gameName string,
	width int,
	height int,
) Model {
	model := New(appService, manager, width, height)
	model.screen = screenThread
	model.directThread = true
	model.selectedTeam = &team
	model.selectedGame = gameSlug
	model.thread = &conversationcore.ThreadView{Conversation: &stake.ApprovalConversation{
		Team: team,
		Game: stake.ApprovalOperator{Slug: gameSlug, Name: gameName},
	}}
	return model
}

func (m Model) Init() tea.Cmd {
	if m.directThread {
		return tea.Batch(m.spinner.Tick, m.loadThread(true))
	}
	return tea.Batch(m.spinner.Tick, m.loadTeams())
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case teamsLoadedMsg:
		m.loading = false
		m.teams, m.err = msg.teams, msg.err
		return m, nil
	case threadsLoadedMsg:
		if m.selectedTeam == nil || msg.team != m.selectedTeam.Slug {
			return m, nil
		}
		m.loading, m.refreshing = false, false
		m.err = msg.err
		if msg.err == nil {
			m.threads = msg.result.Conversations
			m.lastRefresh = time.Now()
			m.status = hookStatus(msg.result)
			m.normalizeThreadCursor()
		}
		return m, m.ensureRefreshScheduled(listRefreshInterval)
	case threadLoadedMsg:
		if msg.team != m.selectedTeamSlug() || msg.game != m.selectedGame {
			return m, nil
		}
		m.loading, m.refreshing = false, false
		m.err = msg.err
		if msg.err == nil {
			atBottom := len(m.timelineBlocks) == 0 || m.viewport.AtBottom()
			m.thread = msg.thread
			m.lastRefresh = time.Now()
			m.setTimelineContent()
			if atBottom {
				m.timelineCursor = max(0, len(m.timelineBlocks)-1)
				m.renderTimelineContent()
				m.viewport.GotoBottom()
			}
		}
		return m, m.ensureRefreshScheduled(threadRefreshInterval)
	case messageSentMsg:
		m.sending = false
		if msg.err != nil {
			m.err = msg.err
			m.status = "Reply failed"
			return m, nil
		}
		m.composer = false
		m.textarea.Blur()
		m.textarea.Reset()
		m.thread = msg.thread
		m.err = nil
		m.status = "Reply sent"
		m.setTimelineContent()
		m.timelineCursor = max(0, len(m.timelineBlocks)-1)
		m.renderTimelineContent()
		m.viewport.GotoBottom()
		return m, nil
	case readChangedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if m.thread != nil {
			m.thread.Stats = msg.stats
		}
		if msg.read {
			m.status = "All messages marked read"
		} else {
			m.status = "All messages marked unread"
		}
		return m, nil
	case refreshTickMsg:
		if msg.generation != m.refreshGeneration {
			return m, nil
		}
		m.refreshScheduled = false
		next := m.ensureRefreshScheduled(m.refreshInterval())
		if m.loading || m.refreshing || m.sending || m.composer {
			return m, next
		}
		m.refreshing = true
		if m.screen == screenThreads && m.selectedTeam != nil {
			return m, tea.Batch(next, m.loadThreads())
		}
		if m.screen == screenThread && m.selectedTeam != nil && m.selectedGame != "" {
			return m, tea.Batch(next, m.loadThread(m.viewport.AtBottom()))
		}
		m.refreshing = false
		return m, next
	case spinner.TickMsg:
		if m.loading || m.refreshing || m.sending {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	if m.composer {
		return m.updateComposer(msg)
	}
	if m.searching {
		return m.updateSearch(msg)
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key.String())
	}
	if m.screen == screenThread {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateSearch(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "enter":
			m.searching = false
			m.search.Blur()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.threadCursor = 0
	return m, cmd
}

func (m Model) updateComposer(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.composer = false
			m.textarea.Blur()
			m.status = "Draft kept in memory"
			return m, nil
		case "ctrl+p":
			m.private = !m.private
			return m, nil
		case "ctrl+s":
			message := strings.TrimSpace(m.textarea.Value())
			if message == "" || m.sending {
				return m, nil
			}
			m.sending = true
			return m, m.sendMessage(message)
		}
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m Model) handleKey(key string) (Model, tea.Cmd) {
	switch m.screen {
	case screenTeams:
		switch key {
		case "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "up", "k":
			if m.teamCursor > 0 {
				m.teamCursor--
			}
		case "down", "j":
			if m.teamCursor < len(m.teams)-1 {
				m.teamCursor++
			}
		case "enter":
			if len(m.teams) > 0 {
				team := m.teams[m.teamCursor]
				m.selectedTeam = &team
				m.screen = screenThreads
				m.bumpRefreshGeneration()
				m.loading = true
				m.err = nil
				return m, tea.Batch(m.spinner.Tick, m.loadThreads())
			}
		case "r":
			m.loading = true
			return m, tea.Batch(m.spinner.Tick, m.loadTeams())
		}
	case screenThreads:
		switch key {
		case "esc":
			m.screen = screenTeams
			m.bumpRefreshGeneration()
			m.selectedTeam = nil
			m.threads = nil
			m.err = nil
			return m, nil
		case "up", "k":
			if m.threadCursor > 0 {
				m.threadCursor--
			}
		case "down", "j":
			if m.threadCursor < len(m.visibleThreads())-1 {
				m.threadCursor++
			}
		case "enter":
			visible := m.visibleThreads()
			if len(visible) > 0 {
				m.selectedGame = visible[m.threadCursor].GameSlug
				m.screen = screenThread
				m.bumpRefreshGeneration()
				m.loading = true
				m.err = nil
				return m, tea.Batch(m.spinner.Tick, m.loadThread(true))
			}
		case "u":
			m.unreadOnly = !m.unreadOnly
			m.threadCursor = 0
		case "/":
			m.searching = true
			return m, m.search.Focus()
		case "x":
			m.search.Reset()
			m.threadCursor = 0
		case "r":
			if !m.refreshing {
				m.refreshing = true
				return m, tea.Batch(m.spinner.Tick, m.loadThreads())
			}
		}
	case screenThread:
		switch key {
		case "esc":
			if m.directThread {
				return m, func() tea.Msg { return GoBackMsg{} }
			}
			m.screen = screenThreads
			m.bumpRefreshGeneration()
			m.thread = nil
			m.selectedGame = ""
			m.err = nil
			m.refreshing = true
			return m, m.loadThreads()
		case "c":
			m.composer = true
			m.err = nil
			return m, m.textarea.Focus()
		case "p":
			m.private = !m.private
		case "m":
			return m, m.changeRead(true)
		case "u":
			return m, m.changeRead(false)
		case "r":
			if !m.refreshing {
				m.refreshing = true
				return m, tea.Batch(m.spinner.Tick, m.loadThread(m.viewport.AtBottom()))
			}
		case "up", "k":
			m.moveTimelineCursor(-1)
		case "down", "j":
			m.moveTimelineCursor(1)
		case "pgup", "ctrl+b":
			m.moveTimelineCursor(-5)
		case "pgdown", "ctrl+f":
			m.moveTimelineCursor(5)
		case "home", "g":
			m.setTimelineCursor(0)
		case "end", "G":
			m.setTimelineCursor(len(m.timelineBlocks) - 1)
		case "n":
			m.jumpToMessage(1)
		case "N":
			m.jumpToMessage(-1)
		case "enter", "e", " ":
			m.toggleTimelineGroup()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	var builder strings.Builder
	if m.directThread {
		builder.WriteString("\n  Approval Thread")
		if m.thread != nil && m.thread.Conversation != nil && m.thread.Conversation.Game.Name != "" {
			fmt.Fprintf(&builder, " — %s", m.thread.Conversation.Game.Name)
		}
	} else {
		builder.WriteString("\n  Conversations")
	}
	if !m.directThread && m.selectedTeam != nil {
		fmt.Fprintf(&builder, " — %s", m.selectedTeam.Name)
	}
	builder.WriteString("\n\n")
	if m.loading {
		fmt.Fprintf(&builder, "  %s Loading…\n", m.spinner.View())
		return builder.String()
	}
	if m.err != nil {
		fmt.Fprintf(&builder, "  Error: %s\n\n", m.err)
	}
	switch m.screen {
	case screenTeams:
		m.viewTeams(&builder)
	case screenThreads:
		m.viewThreads(&builder)
	case screenThread:
		m.viewThread(&builder)
	}
	return builder.String()
}

func (m Model) viewTeams(builder *strings.Builder) {
	if len(m.teams) == 0 {
		builder.WriteString("  No teams found.\n")
	}
	for index, team := range m.teams {
		cursor := "  "
		if index == m.teamCursor {
			cursor = "> "
		}
		fmt.Fprintf(builder, "  %s%-28s  %s\n", cursor, compact(team.Name, 28), team.Slug)
	}
	builder.WriteString("\n  Enter: conversations • r: refresh • Esc: back\n")
}

func (m Model) viewThreads(builder *strings.Builder) {
	threads := m.visibleThreads()
	filter := "all"
	if m.unreadOnly {
		filter = "unread"
	}
	query := strings.TrimSpace(m.search.Value())
	if m.searching {
		fmt.Fprintf(builder, "  Filter: %s • Search: %s\n", filter, m.search.View())
		builder.WriteString("\n")
	} else if query != "" {
		fmt.Fprintf(builder, "  Filter: %s • Search: %q • refreshed %s\n\n", filter, query, refreshAge(m.lastRefresh))
	} else {
		fmt.Fprintf(builder, "  Filter: %s • refreshed %s\n\n", filter, refreshAge(m.lastRefresh))
	}
	if len(threads) == 0 {
		builder.WriteString("  No matching approval conversations.\n")
	}
	for index, thread := range threads {
		cursor := "  "
		if index == m.threadCursor {
			cursor = "> "
		}
		state := "closed"
		if thread.Open {
			state = "open"
		}
		fmt.Fprintf(builder, "  %s%-24s  unread:%-3d  %-6s  %s\n",
			cursor, compact(thread.GameName, 24), thread.Stats.Unread, state, compact(thread.Preview, 55))
	}
	if m.status != "" {
		fmt.Fprintf(builder, "\n  %s\n", m.status)
	}
	builder.WriteString("\n  Enter: open • /: search • x: clear • u: unread • r: refresh • Esc: teams\n")
}

func (m Model) viewThread(builder *strings.Builder) {
	if m.thread == nil || m.thread.Conversation == nil {
		builder.WriteString("  Conversation is not loaded.\n")
		return
	}
	conversation := m.thread.Conversation
	visibility := "public"
	if m.private {
		visibility = "private"
	}
	fmt.Fprintf(builder, "  %s • unread %d/%d • reply: %s • refreshed %s\n\n",
		conversation.Game.Name, m.thread.Stats.Unread, m.thread.Stats.Total, visibility, refreshAge(m.lastRefresh))
	builder.WriteString(m.viewport.View())
	builder.WriteString("\n")
	if m.composer {
		fmt.Fprintf(builder, "\n  %s reply%s\n", strings.ToUpper(visibility), sendingSuffix(m.sending, m.spinner.View()))
		builder.WriteString(m.textarea.View())
		builder.WriteString("\n  Ctrl+S: send • Ctrl+P: visibility • Esc: close composer\n")
	} else {
		if m.status != "" {
			fmt.Fprintf(builder, "  %s\n", m.status)
		}
		builder.WriteString("  j/k: blocks • n/N: messages • Enter/e: expand • c: reply • m/u: read state • r: refresh • Esc: back\n")
	}
}

func (m *Model) resize() {
	m.textarea.SetWidth(max(30, m.width-8))
	m.search.SetWidth(max(20, min(50, m.width-18)))
	m.viewport.SetWidth(max(30, m.width-4))
	m.viewport.SetHeight(max(8, m.height-10))
	if m.thread != nil {
		m.setTimelineContent()
	}
}

func (m *Model) setTimelineContent() {
	if m.thread == nil || m.thread.Conversation == nil {
		m.timelineBlocks = nil
		m.timelineOffsets = nil
		m.viewport.SetContent("")
		return
	}
	selectedKey := ""
	if m.timelineCursor >= 0 && m.timelineCursor < len(m.timelineBlocks) {
		selectedKey = m.timelineBlocks[m.timelineCursor].key
	}
	m.timelineBlocks = buildTimelineBlocks(m.thread.Conversation.Events)
	if selectedKey != "" {
		for index, block := range m.timelineBlocks {
			if block.key == selectedKey {
				m.timelineCursor = index
				break
			}
		}
	}
	if len(m.timelineBlocks) == 0 {
		m.timelineCursor = 0
	} else if m.timelineCursor >= len(m.timelineBlocks) {
		m.timelineCursor = len(m.timelineBlocks) - 1
	}
	m.renderTimelineContent()
}

func (m Model) visibleThreads() []conversationcore.ThreadSummary {
	result := make([]conversationcore.ThreadSummary, 0, len(m.threads))
	query := strings.ToLower(strings.TrimSpace(m.search.Value()))
	for _, thread := range m.threads {
		if m.unreadOnly && thread.Stats.Unread == 0 {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(thread.GameName+" "+thread.GameSlug+" "+thread.Preview), query) {
			continue
		}
		result = append(result, thread)
	}
	return result
}

func (m *Model) normalizeThreadCursor() {
	visible := m.visibleThreads()
	if len(visible) == 0 {
		m.threadCursor = 0
	} else if m.threadCursor >= len(visible) {
		m.threadCursor = len(visible) - 1
	}
}

func (m Model) loadTeams() tea.Cmd {
	appService := m.service
	return func() tea.Msg {
		teams, err := appService.ListTeams(context.Background())
		return teamsLoadedMsg{teams: teams, err: err}
	}
}

func (m Model) loadThreads() tea.Cmd {
	manager, team := m.manager, m.selectedTeamSlug()
	return func() tea.Msg {
		result, err := manager.RefreshTeam(context.Background(), team)
		return threadsLoadedMsg{team: team, result: result, err: err}
	}
}

func (m Model) loadThread(markRead bool) tea.Cmd {
	manager, team, game := m.manager, m.selectedTeamSlug(), m.selectedGame
	return func() tea.Msg {
		thread, err := manager.GetThread(context.Background(), team, game, markRead)
		return threadLoadedMsg{team: team, game: game, thread: thread, err: err}
	}
}

func (m Model) sendMessage(message string) tea.Cmd {
	manager, team, game, private := m.manager, m.selectedTeamSlug(), m.selectedGame, m.private
	return func() tea.Msg {
		thread, err := manager.SendMessage(context.Background(), team, game, message, private, nil)
		return messageSentMsg{team: team, game: game, thread: thread, err: err}
	}
}

func (m Model) changeRead(read bool) tea.Cmd {
	manager, team, game := m.manager, m.selectedTeamSlug(), m.selectedGame
	return func() tea.Msg {
		stats, err := manager.SetRead(context.Background(), team, game, nil, read)
		return readChangedMsg{read: read, stats: stats, err: err}
	}
}

func (m Model) selectedTeamSlug() string {
	if m.selectedTeam == nil {
		return ""
	}
	return m.selectedTeam.Slug
}

func (m Model) refreshInterval() time.Duration {
	if m.screen == screenThread {
		return threadRefreshInterval
	}
	return listRefreshInterval
}

func (m *Model) ensureRefreshScheduled(interval time.Duration) tea.Cmd {
	if m.refreshScheduled {
		return nil
	}
	m.refreshScheduled = true
	generation := m.refreshGeneration
	return tea.Tick(interval, func(time.Time) tea.Msg { return refreshTickMsg{generation: generation} })
}

func (m *Model) bumpRefreshGeneration() {
	m.refreshGeneration++
	m.refreshScheduled = false
}

func systemEventSummary(event stake.ApprovalEvent) string {
	switch event.Type {
	case "version_publish":
		return fmt.Sprintf("Published %s version %s", event.Data.Kind, intValue(event.Data.Version))
	case "version_change":
		return fmt.Sprintf("Changed %s version to %s", event.Data.Kind, intValue(event.Data.Version))
	case "rating", "rating_submission":
		return "Rating: " + intValue(event.Data.Rating)
	case "assigned_approver":
		return "Assigned approver: " + event.Data.Name
	case "guideline_checked":
		return "Checked guideline: " + event.Data.Text
	case "guideline_unchecked":
		return "Unchecked guideline: " + event.Data.Text
	case "active", "approval":
		if event.Data.Operator != nil {
			return fmt.Sprintf("%s • %s v%s", event.Data.Operator.Name, event.Data.Kind, intValue(event.Data.Version))
		}
	case "status_change":
		if event.Data.Open != nil {
			return fmt.Sprintf("Open: %t", *event.Data.Open)
		}
	}
	return ""
}

func intValue(value *int) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *value)
}

func formatEventTime(value int64) string {
	if value == 0 {
		return "unknown time"
	}
	if value < 100_000_000_000 {
		return time.Unix(value, 0).Local().Format("2006-01-02 15:04")
	}
	return time.UnixMilli(value).Local().Format("2006-01-02 15:04")
}

func refreshAge(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Format("15:04:05")
}

func hookStatus(result conversationcore.RefreshResult) string {
	var succeeded, failed int
	for _, hook := range result.Hooks {
		if hook.Success {
			succeeded++
		} else {
			failed++
		}
	}
	parts := make([]string, 0, 2)
	if succeeded > 0 || failed > 0 {
		parts = append(parts, fmt.Sprintf("hooks: %d succeeded, %d failed", succeeded, failed))
	}
	if len(result.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("%d conversations failed to refresh", len(result.Errors)))
	}
	return strings.Join(parts, " • ")
}

func sendingSuffix(sending bool, spinnerView string) string {
	if !sending {
		return ""
	}
	return " " + spinnerView + " sending"
}

func wrapText(value string, width int) []string {
	var result []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			result = append(result, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width {
				line += " " + word
			} else {
				result = append(result, line)
				line = word
			}
		}
		result = append(result, line)
	}
	return result
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

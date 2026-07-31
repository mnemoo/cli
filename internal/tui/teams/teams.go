package teams

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

var (
	profitStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	expectedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	selectedProfitStyle   = profitStyle.Bold(true).Background(lipgloss.Color("8"))
	selectedExpectedStyle = expectedStyle.Bold(true).Background(lipgloss.Color("8"))
)

type TeamSelectedMsg struct {
	Team stake.Team
}

type GoBackMsg struct{}

type teamsLoadedMsg struct {
	teams     []stake.Team
	overviews map[string]service.TeamOverview
}

type teamsErrorMsg struct {
	err error
}

type state int

const (
	stateLoading state = iota
	stateList
	stateError
)

type Model struct {
	service   *service.Service
	teams     []stake.Team
	overviews map[string]service.TeamOverview
	cursor    int
	state     state
	spinner   spinner.Model
	err       error
	width     int
	height    int
}

func New(appService *service.Service) Model {
	return Model{
		service: appService,
		state:   stateLoading,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadTeams())
}

func (m Model) loadTeams() tea.Cmd {
	appService := m.service
	return func() tea.Msg {
		ctx := context.Background()
		overviews, err := appService.ListTeamOverviews(ctx)
		if err != nil {
			return teamsErrorMsg{err: err}
		}
		teams := make([]stake.Team, len(overviews))
		bySlug := make(map[string]service.TeamOverview, len(overviews))
		for i, overview := range overviews {
			teams[i] = overview.Team
			bySlug[overview.Team.Slug] = overview
		}
		return teamsLoadedMsg{teams: teams, overviews: bySlug}
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case teamsLoadedMsg:
		m.teams = msg.teams
		m.overviews = msg.overviews
		m.state = stateList
		return m, nil

	case teamsErrorMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		if m.state == stateLoading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()

	switch m.state {
	case stateList:
		switch key {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.teams)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.teams) > 0 {
				team := m.teams[m.cursor]
				return m, func() tea.Msg { return TeamSelectedMsg{Team: team} }
			}
		}

	case stateError:
		switch key {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "r":
			m.state = stateLoading
			return m, tea.Batch(m.spinner.Tick, m.loadTeams())
		}
	}

	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n  Teams\n\n")

	switch m.state {
	case stateLoading:
		fmt.Fprintf(&b, "  %s Loading teams...\n", m.spinner.View())

	case stateList:
		if len(m.teams) == 0 {
			b.WriteString("  No teams found.\n")
		} else {
			fmt.Fprintf(&b, "  %-2s %-22s %10s %18s %18s %14s\n",
				"", "Team", "Online", "Profit Share (10%)", "Expected (7.5%)", "Carry")
			b.WriteString("  " + strings.Repeat("─", 91) + "\n")

			for i, t := range m.teams {
				cursor := "  "
				if i == m.cursor {
					cursor = "> "
				}

				profitShare := "—"
				expected := "—"
				carry := "—"
				online := "—"
				method := ""
				if overview, ok := m.overviews[t.Slug]; ok {
					if overview.Earnings != nil {
						profitShare = formatBalance(overview.Earnings.ProfitShare)
						expected = formatBalance(overview.Earnings.ExpectedProfit)
					}
					if overview.Balance != nil {
						carry = formatBalance(overview.Balance.Carry)
					}
					if overview.PaymentMethod != nil {
						method = overview.PaymentMethod.Current
					}
					if overview.OnlinePlayers != nil {
						online = formatCount(*overview.OnlinePlayers)
					}
				}

				name := t.Name
				if len(name) > 22 {
					name = name[:21] + "…"
				}

				profitMarker, expectedMarker := "  ", "  "
				profitCellStyle, expectedCellStyle := profitStyle, expectedStyle
				switch method {
				case "profit":
					profitMarker = "● "
					profitCellStyle = selectedProfitStyle
				case "turnover":
					expectedMarker = "● "
					expectedCellStyle = selectedExpectedStyle
				}
				profitCell := profitCellStyle.Render(fmt.Sprintf("%s%16s", profitMarker, profitShare))
				expectedCell := expectedCellStyle.Render(fmt.Sprintf("%s%16s", expectedMarker, expected))

				fmt.Fprintf(&b, "  %s%-22s %10s %s %s %14s\n",
					cursor, name, online, profitCell, expectedCell, carry)
			}
		}

		b.WriteString("\n  ● selected payment model • Enter: select • Esc: back\n")

	case stateError:
		fmt.Fprintf(&b, "  Error: %s\n\n", m.err.Error())
		b.WriteString("  r: retry • Esc: back\n")
	}

	return b.String()
}

func formatBalance(raw float64) string {
	val := raw / 1e6
	if val >= 0 {
		return fmt.Sprintf("$%.2f", val)
	}
	return fmt.Sprintf("-$%.2f", -val)
}

func formatCount(value int) string {
	raw := fmt.Sprintf("%d", value)
	for i := len(raw) - 3; i > 0; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	return raw
}

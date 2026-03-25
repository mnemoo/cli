package teams

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/api"
)

type TeamSelectedMsg struct {
	Team api.TeamListItem
}

type GoBackMsg struct{}

type teamsLoadedMsg struct {
	teams    []api.TeamListItem
	balances map[string]*api.TeamBalance
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
	client   *api.Client
	teams    []api.TeamListItem
	balances map[string]*api.TeamBalance
	cursor   int
	state    state
	spinner  spinner.Model
	err      error
	width    int
	height   int
}

func New(client *api.Client) Model {
	return Model{
		client:  client,
		state:   stateLoading,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadTeams())
}

func (m Model) loadTeams() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx := context.Background()
		teams, err := client.ListTeams(ctx)
		if err != nil {
			return teamsErrorMsg{err: err}
		}

		balances := make(map[string]*api.TeamBalance, len(teams))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, t := range teams {
			wg.Add(1)
			go func(slug string) {
				defer wg.Done()
				b, err := client.GetTeamBalance(ctx, slug)
				if err != nil {
					return
				}
				mu.Lock()
				balances[slug] = b
				mu.Unlock()
			}(t.Slug)
		}
		wg.Wait()

		return teamsLoadedMsg{teams: teams, balances: balances}
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
		m.balances = msg.balances
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
		b.WriteString(fmt.Sprintf("  %s Loading teams...\n", m.spinner.View()))

	case stateList:
		if len(m.teams) == 0 {
			b.WriteString("  No teams found.\n")
		} else {
			for i, t := range m.teams {
				cursor := "  "
				if i == m.cursor {
					cursor = "> "
				}

				standings := "—"
				if bal, ok := m.balances[t.Slug]; ok {
					standings = formatBalance(bal.Position)
				}

				b.WriteString(fmt.Sprintf("  %s%-20s  Standings: %s\n",
					cursor, t.Name, standings))
			}
		}

		b.WriteString("\n  Enter: select • Esc: back\n")

	case stateError:
		b.WriteString(fmt.Sprintf("  Error: %s\n\n", m.err.Error()))
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

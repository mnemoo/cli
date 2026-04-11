package games

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/api"
)

type GameSelectedMsg struct {
	Game api.TeamGameCard
}

type GoBackMsg struct{}

type gamesLoadedMsg struct {
	games []api.TeamGameCard
}

type gamesErrorMsg struct {
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
	teamSlug string
	teamName string
	games    []api.TeamGameCard
	cursor   int
	showDay  bool
	state    state
	spinner  spinner.Model
	err      error
	width    int
	height   int
}

func New(client *api.Client, teamSlug, teamName string) Model {
	return Model{
		client:   client,
		teamSlug: teamSlug,
		teamName: teamName,
		state:    stateLoading,
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadGames())
}

func (m Model) loadGames() tea.Cmd {
	client := m.client
	slug := m.teamSlug
	return func() tea.Msg {
		games, err := client.ListTeamGames(context.Background(), slug)
		if err != nil {
			return gamesErrorMsg{err: err}
		}
		return gamesLoadedMsg{games: games}
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case gamesLoadedMsg:
		m.games = msg.games
		m.state = stateList
		return m, nil

	case gamesErrorMsg:
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
			if m.cursor < len(m.games)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.games) > 0 {
				game := m.games[m.cursor]
				return m, func() tea.Msg { return GameSelectedMsg{Game: game} }
			}
		case "tab":
			m.showDay = !m.showDay
		}

	case stateError:
		switch key {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg { return GoBackMsg{} }
		case "r":
			m.state = stateLoading
			return m, tea.Batch(m.spinner.Tick, m.loadGames())
		}
	}

	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n  Games — %s\n\n", m.teamName)

	switch m.state {
	case stateLoading:
		fmt.Fprintf(&b, "  %s Loading games...\n", m.spinner.View())

	case stateList:
		if len(m.games) == 0 {
			b.WriteString("  No games found.\n")
		} else {
			periodLabel := "Month Profit"
			if m.showDay {
				periodLabel = "Day Profit"
			}

			header := fmt.Sprintf("  %-3s %-24s %-8s %-8s %-14s %s\n",
				"#", "Name", "Rating", "Online", periodLabel, "Turnover")
			b.WriteString(header)
			b.WriteString("  " + strings.Repeat("─", 78) + "\n")

			for i, g := range m.games {
				cursor := " "
				if i == m.cursor {
					cursor = ">"
				}

				rating := ratingStars(g.Rating)
				profit := "—"
				turnover := "—"

				if g.Stats != nil {
					period := g.Stats.Month
					if m.showDay {
						period = g.Stats.Day
					}
					if period != nil {
						profit = formatProfit(period.Profit)
						turnover = formatTurnover(period.Turnover)
					}
				}

				name := g.Name
				if len(name) > 22 {
					name = name[:22] + "…"
				}

				fmt.Fprintf(&b, "  %s%-3d %-24s %-8s %-8d %-14s %s\n",
					cursor, i+1, name, rating, g.OnlinePlayers, profit, turnover)
			}
		}

		hint := "Day"
		if m.showDay {
			hint = "Month"
		}
		fmt.Fprintf(&b, "\n  Enter: details • Tab: switch to %s • Esc: back\n", hint)

	case stateError:
		fmt.Fprintf(&b, "  Error: %s\n\n", m.err.Error())
		b.WriteString("  r: retry • Esc: back\n")
	}

	return b.String()
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

func formatProfit(raw float64) string {
	val := raw / 1e7
	if val >= 0 {
		return fmt.Sprintf("$%.2f", val)
	}
	return fmt.Sprintf("-$%.2f", -val)
}

func formatTurnover(raw float64) string {
	val := raw / 1e6
	if val >= 0 {
		return fmt.Sprintf("$%.2f", val)
	}
	return fmt.Sprintf("-$%.2f", -val)
}

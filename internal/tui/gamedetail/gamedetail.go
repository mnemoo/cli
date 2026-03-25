package gamedetail

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/api"
)

type GoBackMsg struct{}

type detailLoadedMsg struct {
	detail   *api.TeamGameDetail
	versions []api.GameVersionHistoryItem
}

type detailErrorMsg struct {
	err error
}

type state int

const (
	stateLoading state = iota
	stateReady
	stateError
)

type Model struct {
	client   *api.Client
	teamSlug string
	gameSlug string
	gameName string
	detail   *api.TeamGameDetail
	versions []api.GameVersionHistoryItem
	state    state
	spinner  spinner.Model
	err      error
	width    int
	height   int
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
		detail, err := client.GetGameDetail(context.Background(), teamSlug, gameSlug)
		if err != nil {
			return detailErrorMsg{err: err}
		}
		versions, err := client.GetGameVersions(context.Background(), teamSlug, gameSlug)
		if err != nil {
			return detailErrorMsg{err: err}
		}
		return detailLoadedMsg{detail: detail, versions: versions}
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
		m.state = stateReady
		return m, nil

	case detailErrorMsg:
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

	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, func() tea.Msg { return GoBackMsg{} }
	}

	if m.state == stateError && key == "r" {
		m.state = stateLoading
		return m, tea.Batch(m.spinner.Tick, m.loadDetail())
	}

	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("\n  Game — %s\n\n", m.gameName))

	switch m.state {
	case stateLoading:
		b.WriteString(fmt.Sprintf("  %s Loading...\n", m.spinner.View()))

	case stateReady:
		d := m.detail
		b.WriteString(fmt.Sprintf("  Name:     %s\n", d.Name))
		b.WriteString(fmt.Sprintf("  Slug:     %s\n", d.Slug))
		b.WriteString(fmt.Sprintf("  Rating:   %s\n", ratingStars(d.Rating)))

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

		b.WriteString("\n  Versions\n")
		if len(m.versions) == 0 {
			b.WriteString("  No versions published.\n")
		} else {
			b.WriteString(fmt.Sprintf("  %-8s %-8s %-22s %s\n", "Type", "Ver", "Created", "Operators"))
			b.WriteString("  " + strings.Repeat("─", 70) + "\n")
			for _, v := range m.versions {
				created := time.Unix(int64(v.Created)/1000, 0).Format("2006-01-02 15:04")
				ops := operatorsList(v.Approved)
				b.WriteString(fmt.Sprintf("  %-8s v%-7d %-22s %s\n", v.Type, v.Version, created, ops))
			}
		}

		b.WriteString("\n  Esc: back\n")

	case stateError:
		b.WriteString(fmt.Sprintf("  Error: %s\n\n", m.err.Error()))
		b.WriteString("  r: retry • Esc: back\n")
	}

	return b.String()
}

func ratingStars(rating *float64) string {
	if rating == nil {
		return "—"
	}
	stars := int(math.Round(*rating / 333.0))
	if stars > 3 {
		stars = 3
	}
	if stars <= 0 {
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

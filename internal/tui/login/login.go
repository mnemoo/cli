package login

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mnemoo/cli/internal/auth"
)

type LoginSuccessMsg struct {
	User *auth.User
}

type loginErrorMsg struct {
	err error
}

type state int

const (
	stateInput state = iota
	stateLoading
	stateError
)

type Model struct {
	input   textinput.Model
	spinner spinner.Model
	state   state
	err     error
	width   int
	height  int
}

func New() Model {
	ti := textinput.New()
	ti.Placeholder = "Enter SID..."
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.SetWidth(64)
	ti.Focus()

	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
	)

	return Model{
		input:   ti,
		spinner: sp,
		state:   stateInput,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), m.spinner.Tick)
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.state == stateInput {
				sid := strings.TrimSpace(m.input.Value())
				if sid == "" {
					return m, nil
				}
				m.state = stateLoading
				m.err = nil
				return m, tea.Batch(m.spinner.Tick, doLogin(sid))
			}
			if m.state == stateError {
				m.state = stateInput
				m.err = nil
				m.input.SetValue("")
				return m, m.input.Focus()
			}
		case "esc":
			if m.state == stateError {
				m.state = stateInput
				m.err = nil
				return m, m.input.Focus()
			}
		}

	case LoginSuccessMsg:
		return m, nil

	case loginErrorMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if m.state == stateLoading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if m.state == stateInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFA500"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
	greenStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#34D399"))
	cyanStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	hintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
)

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + titleStyle.Render("Stake CLI") + "\n\n")
	b.WriteString("  " + dimStyle.Render("An") + " " + greenStyle.Render("open-source") + " " +
		dimStyle.Render("tool by") + " " + accentStyle.Render("Mnemoo") + ". " +
		dimStyle.Render("Built") + " " + cyanStyle.Render("privacy & safety first") + "\n")
	b.WriteString("  " + dimStyle.Render("That tool is not affiliated with the Stake Engine team.") + "\n\n")

	switch m.state {
	case stateInput:
		b.WriteString("  Enter your session ID (SID):\n\n")
		b.WriteString("  " + m.input.View() + "\n\n")
		b.WriteString("  " + hintStyle.Render("Read the README.md file for gathering session ID guide.") + "\n")
		b.WriteString("  " + hintStyle.Render("Press Enter to login • Ctrl+C to quit") + "\n")

	case stateLoading:
		b.WriteString(fmt.Sprintf("  %s Validating session...\n", m.spinner.View()))

	case stateError:
		b.WriteString("  " + errorStyle.Render(fmt.Sprintf("Error: %s", m.err.Error())) + "\n\n")
		b.WriteString("  " + hintStyle.Render("Press Enter to try again • Ctrl+C to quit") + "\n")
	}

	return b.String()
}

func doLogin(sid string) tea.Cmd {
	return func() tea.Msg {
		user, err := auth.Login(sid)
		if err != nil {
			return loginErrorMsg{err: err}
		}
		return LoginSuccessMsg{User: user}
	}
}

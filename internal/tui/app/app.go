package app

import (
	"fmt"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/auth"
	"github.com/mnemoo/cli/internal/tui/accounts"
	"github.com/mnemoo/cli/internal/tui/login"
)

type screen int

const (
	screenInit screen = iota
	screenLogin
	screenMain
	screenAccounts
)

type initDoneMsg struct {
	user      *auth.User
	needLogin bool
	err       error
}

type Model struct {
	screen   screen
	user     *auth.User
	login    login.Model
	accounts accounts.Model
	spinner  spinner.Model
	width    int
	height   int
	err      error
}

func New() Model {
	return Model{
		screen:  screenInit,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, doInit())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case initDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.needLogin {
			return m.switchToLogin()
		}
		m.user = msg.user
		m.screen = screenMain
		return m, nil
	}

	switch m.screen {
	case screenInit:
		return m.updateInit(msg)
	case screenLogin:
		return m.updateLogin(msg)
	case screenMain:
		return m.updateMain(msg)
	case screenAccounts:
		return m.updateAccounts(msg)
	}

	return m, nil
}

func (m Model) View() tea.View {
	var s string
	switch m.screen {
	case screenInit:
		s = m.viewInit()
	case screenLogin:
		s = m.login.View()
	case screenMain:
		s = m.viewMain()
	case screenAccounts:
		s = m.accounts.View()
	}
	return tea.NewView(s)
}

func (m Model) updateInit(msg tea.Msg) (tea.Model, tea.Cmd) {
	if tick, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(tick)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateLogin(msg tea.Msg) (tea.Model, tea.Cmd) {
	if success, ok := msg.(login.LoginSuccessMsg); ok {
		m.user = success.User
		m.screen = screenMain
		return m, nil
	}

	var cmd tea.Cmd
	m.login, cmd = m.login.Update(msg)
	return m, cmd
}

func (m Model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "q":
			return m, tea.Quit
		case "a":
			return m.switchToAccounts()
		}
	}
	return m, nil
}

func (m Model) updateAccounts(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case accounts.GoBackMsg:
		if msg.ActiveAccount == nil {
			m.user = nil
			return m.switchToLogin()
		}
		m.user = &auth.User{
			ID:    msg.ActiveAccount.ID,
			Name:  msg.ActiveAccount.Name,
			Email: msg.ActiveAccount.Email,
			Image: msg.ActiveAccount.Image,
		}
		m.screen = screenMain
		return m, nil
	case accounts.NeedLoginMsg:
		m.user = nil
		return m.switchToLogin()
	case accounts.SwitchedMsg:
		m.user = msg.User
		m.screen = screenMain
		return m, nil
	default:
		var cmd tea.Cmd
		m.accounts, cmd = m.accounts.Update(msg)
		return m, cmd
	}
}

func (m Model) switchToLogin() (tea.Model, tea.Cmd) {
	m.user = nil
	m.login = login.New()
	m.screen = screenLogin
	return m, m.login.Init()
}

func (m Model) switchToAccounts() (tea.Model, tea.Cmd) {
	m.accounts = accounts.New()
	m.screen = screenAccounts
	return m, m.accounts.Init()
}

func (m Model) viewInit() string {
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %s\n  Press Ctrl+C to exit.\n", m.err.Error())
	}
	return fmt.Sprintf("\n  %s Loading...\n", m.spinner.View())
}

func (m Model) viewMain() string {
	if m.user == nil {
		return "\n  No active session.\n"
	}
	return fmt.Sprintf(
		"\n  Logged in as: %s (%s)\n\n  Press 'a' for accounts • 'q' to quit\n",
		m.user.Name,
		m.user.Email,
	)
}

func doInit() tea.Cmd {
	return func() tea.Msg {
		user, needLogin, err := auth.Init()
		return initDoneMsg{user: user, needLogin: needLogin, err: err}
	}
}

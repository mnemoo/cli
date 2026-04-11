package accounts

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/auth"
)

type SwitchedMsg struct {
	User *auth.User
}

type GoBackMsg struct {
	ActiveAccount *auth.Account
}

type NeedLoginMsg struct{}

type accountDeletedMsg struct{}

type switchErrorMsg struct {
	err error
}

type deleteErrorMsg struct {
	err error
}

type state int

const (
	stateList state = iota
	stateConfirmDelete
	stateSwitching
	stateError
)

type Model struct {
	accounts []auth.Account
	activeID string
	cursor   int
	state    state
	spinner  spinner.Model
	err      error
	width    int
	height   int
}

func New() Model {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	return Model{
		spinner: sp,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadAccounts(), m.spinner.Tick)
}

type accountsLoadedMsg struct {
	accounts []auth.Account
	activeID string
}

func (m Model) loadAccounts() tea.Cmd {
	return func() tea.Msg {
		accs, active, err := auth.ListAccounts()
		if err != nil {
			return deleteErrorMsg{err: err}
		}
		return accountsLoadedMsg{accounts: accs, activeID: active}
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case accountsLoadedMsg:
		m.accounts = msg.accounts
		m.activeID = msg.activeID
		if m.cursor >= len(m.accounts) {
			m.cursor = max(0, len(m.accounts)-1)
		}
		if len(m.accounts) == 0 {
			return m, func() tea.Msg { return NeedLoginMsg{} }
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case SwitchedMsg:
		return m, nil

	case accountDeletedMsg:
		return m, m.loadAccounts()

	case switchErrorMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case deleteErrorMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if m.state == stateSwitching {
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
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg {
				var acc *auth.Account
				if m.activeID != "" {
					for i := range m.accounts {
						if m.accounts[i].ID == m.activeID {
							acc = &m.accounts[i]
							break
						}
					}
				}
				return GoBackMsg{ActiveAccount: acc}
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.accounts)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.accounts) == 0 {
				return m, nil
			}
			acc := m.accounts[m.cursor]
			if acc.ID == m.activeID {
				return m, nil
			}
			m.state = stateSwitching
			return m, tea.Batch(m.spinner.Tick, doSwitch(acc.ID))
		case "d":
			if len(m.accounts) > 0 {
				m.state = stateConfirmDelete
			}
		case "a":
			return m, func() tea.Msg { return NeedLoginMsg{} }
		}

	case stateConfirmDelete:
		switch key {
		case "y":
			acc := m.accounts[m.cursor]
			m.state = stateList
			return m, doDelete(acc.ID)
		case "n", "esc":
			m.state = stateList
		case "ctrl+c":
			return m, tea.Quit
		}

	case stateError:
		switch key {
		case "enter", "esc":
			m.state = stateList
			m.err = nil
		case "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n  Accounts\n\n")

	switch m.state {
	case stateList, stateConfirmDelete:
		if len(m.accounts) == 0 {
			b.WriteString("  No accounts. Press 'a' to add one.\n")
		} else {
			for i, acc := range m.accounts {
				cursor := "  "
				if i == m.cursor {
					cursor = "> "
				}
				active := ""
				if acc.ID == m.activeID {
					active = " (active)"
				}
				fmt.Fprintf(&b, "  %s%s <%s>%s\n", cursor, acc.Name, acc.Email, active)
			}
		}

		b.WriteString("\n")

		if m.state == stateConfirmDelete && len(m.accounts) > 0 {
			acc := m.accounts[m.cursor]
			fmt.Fprintf(&b, "  Delete %s? (y/n)\n", acc.Name)
		} else {
			b.WriteString("  Enter: switch • a: add • d: delete • Esc: back\n")
		}

	case stateSwitching:
		fmt.Fprintf(&b, "  %s Switching account...\n", m.spinner.View())

	case stateError:
		fmt.Fprintf(&b, "  Error: %s\n\n", m.err.Error())
		b.WriteString("  Press Enter to continue\n")
	}

	return b.String()
}

func doSwitch(accountID string) tea.Cmd {
	return func() tea.Msg {
		user, err := auth.SwitchAccount(accountID)
		if err != nil {
			return switchErrorMsg{err: err}
		}
		return SwitchedMsg{User: user}
	}
}

func doDelete(accountID string) tea.Cmd {
	return func() tea.Msg {
		if err := auth.Logout(accountID); err != nil {
			return deleteErrorMsg{err: err}
		}
		return accountDeletedMsg{}
	}
}

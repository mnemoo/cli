package app

import (
	"fmt"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
	conversationcore "github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/tui/accounts"
	conversationui "github.com/mnemoo/cli/internal/tui/conversations"
	"github.com/mnemoo/cli/internal/tui/gamedetail"
	"github.com/mnemoo/cli/internal/tui/games"
	"github.com/mnemoo/cli/internal/tui/login"
	"github.com/mnemoo/cli/internal/tui/teams"
	uploadui "github.com/mnemoo/cli/internal/tui/upload"
	coreupload "github.com/mnemoo/cli/internal/upload"
)

type screen int

type mode int

const (
	modeDefault mode = iota
	modePublish
)

const (
	screenInit screen = iota
	screenLogin
	screenMain
	screenAccounts
	screenTeams
	screenGames
	screenGameDetail
	screenUpload
	screenConversations
)

type initDoneMsg struct {
	user      *auth.User
	needLogin bool
	err       error
}

type menuItem struct {
	label string
}

var mainMenu = []menuItem{
	{label: "Teams"},
	{label: "Messages"},
}

type Model struct {
	mode                mode
	publishType         string
	publishPath         string
	publishCompliance   coreupload.ComplianceOptions
	updateApproval      bool
	screen              screen
	user                *auth.User
	login               login.Model
	accounts            accounts.Model
	teams               teams.Model
	games               games.Model
	gameDetail          gamedetail.Model
	upload              uploadui.Model
	conversations       conversationui.Model
	spinner             spinner.Model
	service             *service.Service
	conversationManager *conversationcore.Manager
	conversationReturn  screen
	selectedTeam        *stake.Team
	selectedGame        *stake.Game
	mainCursor          int
	width               int
	height              int
	err                 error
}

func New() Model {
	return Model{
		screen:  screenInit,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func NewPublish(uploadType, path string, compliance coreupload.ComplianceOptions, updateApproval bool) Model {
	return Model{
		mode:              modePublish,
		publishType:       uploadType,
		publishPath:       path,
		publishCompliance: compliance,
		updateApproval:    updateApproval,
		screen:            screenAccounts,
		accounts:          accounts.NewSelector(),
		spinner:           spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

// Cleanup removes transient files owned by nested views.
func (m Model) Cleanup() {
	m.upload.Cleanup()
}

func (m Model) Init() tea.Cmd {
	if m.mode == modePublish {
		return m.accounts.Init()
	}
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
		m.initAPIClient()
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
	case screenTeams:
		return m.updateTeams(msg)
	case screenGames:
		return m.updateGames(msg)
	case screenGameDetail:
		return m.updateGameDetail(msg)
	case screenUpload:
		return m.updateUpload(msg)
	case screenConversations:
		return m.updateConversations(msg)
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
	case screenTeams:
		s = m.teams.View()
	case screenGames:
		s = m.games.View()
	case screenGameDetail:
		s = m.gameDetail.View()
	case screenUpload:
		s = m.upload.View()
	case screenConversations:
		s = m.conversations.View()
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
		m.initAPIClient()
		if m.mode == modePublish {
			return m.switchToTeams()
		}
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
		case "up", "k":
			if m.mainCursor > 0 {
				m.mainCursor--
			}
		case "down", "j":
			if m.mainCursor < len(mainMenu)-1 {
				m.mainCursor++
			}
		case "enter":
			return m.handleMenuSelect()
		}
	}
	return m, nil
}

func (m Model) handleMenuSelect() (tea.Model, tea.Cmd) {
	switch m.mainCursor {
	case 0:
		return m.switchToTeams()
	case 1:
		return m.switchToConversations()
	}
	return m, nil
}

func (m Model) updateAccounts(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case accounts.GoBackMsg:
		if m.mode == modePublish {
			return m, tea.Quit
		}
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
		m.initAPIClient()
		m.screen = screenMain
		return m, nil
	case accounts.NeedLoginMsg:
		m.user = nil
		return m.switchToLogin()
	case accounts.SwitchedMsg:
		m.user = msg.User
		m.initAPIClient()
		if m.mode == modePublish {
			return m.switchToTeams()
		}
		m.screen = screenMain
		return m, nil
	default:
		var cmd tea.Cmd
		m.accounts, cmd = m.accounts.Update(msg)
		return m, cmd
	}
}

func (m Model) updateTeams(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case teams.TeamSelectedMsg:
		t := msg.Team
		m.selectedTeam = &t
		return m.switchToGames(t.Slug, t.Name)
	case teams.GoBackMsg:
		if m.mode == modePublish {
			return m.switchToAccounts()
		}
		m.screen = screenMain
		return m, nil
	default:
		var cmd tea.Cmd
		m.teams, cmd = m.teams.Update(msg)
		return m, cmd
	}
}

func (m Model) updateGames(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case games.GameSelectedMsg:
		g := msg.Game
		m.selectedGame = &g
		if m.mode == modePublish {
			return m.switchToPublishUpload(m.selectedTeam.Slug, g.Slug)
		}
		return m.switchToGameDetail(m.selectedTeam.Slug, g.Slug, g.Name)
	case games.GoBackMsg:
		return m.switchToTeams()
	default:
		var cmd tea.Cmd
		m.games, cmd = m.games.Update(msg)
		return m, cmd
	}
}

func (m Model) updateGameDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case gamedetail.GoBackMsg:
		return m.switchToGames(m.selectedTeam.Slug, m.selectedTeam.Name)
	case gamedetail.UploadRequestMsg:
		return m.switchToUpload(msg.TeamSlug, msg.GameSlug)
	case gamedetail.ApprovalThreadRequestMsg:
		return m.switchToGameConversation(msg.TeamSlug, msg.GameSlug, msg.GameName)
	default:
		var cmd tea.Cmd
		m.gameDetail, cmd = m.gameDetail.Update(msg)
		return m, cmd
	}
}

func (m Model) switchToLogin() (tea.Model, tea.Cmd) {
	m.user = nil
	m.service = nil
	m.conversationManager = nil
	m.login = login.New()
	m.screen = screenLogin
	return m, m.login.Init()
}

func (m Model) switchToAccounts() (tea.Model, tea.Cmd) {
	if m.mode == modePublish {
		m.accounts = accounts.NewSelector()
	} else {
		m.accounts = accounts.New()
	}
	m.screen = screenAccounts
	return m, m.accounts.Init()
}

func (m Model) switchToTeams() (tea.Model, tea.Cmd) {
	m.teams = teams.New(m.service)
	m.screen = screenTeams
	return m, m.teams.Init()
}

func (m Model) switchToGames(teamSlug, teamName string) (tea.Model, tea.Cmd) {
	m.games = games.New(m.service, teamSlug, teamName)
	m.screen = screenGames
	return m, m.games.Init()
}

func (m Model) switchToGameDetail(teamSlug, gameSlug, gameName string) (tea.Model, tea.Cmd) {
	m.gameDetail = gamedetail.New(m.service, teamSlug, gameSlug, gameName)
	m.screen = screenGameDetail
	return m, m.gameDetail.Init()
}

func (m Model) updateUpload(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case uploadui.GoBackMsg:
		if m.mode == modePublish {
			return m, tea.Quit
		}
		return m.switchToGameDetail(m.selectedTeam.Slug, m.selectedGame.Slug, m.selectedGame.Name)
	default:
		var cmd tea.Cmd
		m.upload, cmd = m.upload.Update(msg)
		return m, cmd
	}
}

func (m Model) switchToUpload(teamSlug, gameSlug string) (tea.Model, tea.Cmd) {
	m.upload = uploadui.New(m.service, teamSlug, gameSlug, m.width, m.height)
	m.screen = screenUpload
	return m, m.upload.Init()
}

func (m Model) updateConversations(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case conversationui.GoBackMsg:
		m.screen = m.conversationReturn
		return m, nil
	default:
		var cmd tea.Cmd
		m.conversations, cmd = m.conversations.Update(msg)
		return m, cmd
	}
}

func (m Model) switchToConversations() (tea.Model, tea.Cmd) {
	if m.service == nil || m.conversationManager == nil {
		m.err = fmt.Errorf("conversation manager is unavailable")
		return m, nil
	}
	m.conversations = conversationui.New(m.service, m.conversationManager, m.width, m.height)
	m.conversationReturn = screenMain
	m.screen = screenConversations
	return m, m.conversations.Init()
}

func (m Model) switchToGameConversation(teamSlug, gameSlug, gameName string) (tea.Model, tea.Cmd) {
	if m.service == nil || m.conversationManager == nil {
		m.err = fmt.Errorf("conversation manager is unavailable")
		return m, nil
	}
	teamName := ""
	if m.selectedTeam != nil {
		teamName = m.selectedTeam.Name
	}
	m.conversations = conversationui.NewThread(
		m.service,
		m.conversationManager,
		stake.Team{Slug: teamSlug, Name: teamName},
		gameSlug,
		gameName,
		m.width,
		m.height,
	)
	m.conversationReturn = screenGameDetail
	m.screen = screenConversations
	return m, m.conversations.Init()
}

func (m Model) switchToPublishUpload(teamSlug, gameSlug string) (tea.Model, tea.Cmd) {
	m.upload = uploadui.NewPublish(m.service, teamSlug, gameSlug, m.publishType, m.publishPath, m.publishCompliance, m.updateApproval, m.width, m.height)
	m.screen = screenUpload
	return m, m.upload.Init()
}

func (m *Model) initAPIClient() {
	sid, err := auth.GetActiveSID()
	if err != nil {
		return
	}
	client := api.NewClient(sid)
	m.service = service.New(client)
	accountID := ""
	if m.user != nil {
		accountID = m.user.ID
	}
	manager, managerErr := conversationcore.NewDefaultManager(m.service, accountID)
	if managerErr != nil {
		m.err = managerErr
		m.conversationManager = nil
		return
	}
	m.conversationManager = manager
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

	var b string
	b += fmt.Sprintf("\n  Logged in as: %s (%s)\n\n", m.user.Name, m.user.Email)

	for i, item := range mainMenu {
		cursor := "  "
		if i == m.mainCursor {
			cursor = "> "
		}
		b += fmt.Sprintf("  %s%s\n", cursor, item.label)
	}

	b += "\n  Enter: select • a: accounts • q: quit\n"
	return b
}

func doInit() tea.Cmd {
	return func() tea.Msg {
		user, needLogin, err := auth.Init()
		return initDoneMsg{user: user, needLogin: needLogin, err: err}
	}
}

package app

import (
	"testing"

	conversationcore "github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
	conversationui "github.com/mnemoo/cli/internal/tui/conversations"
)

func TestGameConversationReturnsToExistingGameDetail(t *testing.T) {
	model := Model{
		screen: screenGameDetail, service: &service.Service{},
		conversationManager: &conversationcore.Manager{},
		selectedTeam:        &stake.Team{Slug: "team", Name: "Team"},
	}
	openedModel, _ := model.switchToGameConversation("team", "game", "Game")
	opened := openedModel.(Model)
	if opened.screen != screenConversations || opened.conversationReturn != screenGameDetail {
		t.Fatalf("unexpected direct thread route: screen=%d return=%d", opened.screen, opened.conversationReturn)
	}
	returnedModel, _ := opened.updateConversations(conversationui.GoBackMsg{})
	returned := returnedModel.(Model)
	if returned.screen != screenGameDetail {
		t.Fatalf("approval thread returned to screen %d, want game detail", returned.screen)
	}
}

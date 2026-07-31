package teams

import (
	"strings"
	"testing"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

func TestViewShowsTeamStatsTotalsAndSelectedPaymentMethod(t *testing.T) {
	model := Model{
		state: stateList,
		teams: []stake.Team{
			{Name: "Ahead Studio", Slug: "ahead"},
			{Name: "Behind Studio", Slug: "behind"},
		},
		overviews: map[string]service.TeamOverview{
			"ahead": {
				Earnings:      &stake.TeamEarnings{ProfitShare: 12_000_000, ExpectedProfit: 10_000_000},
				Balance:       &stake.TeamBalance{Position: 999_000_000, Carry: 1_000_000},
				PaymentMethod: &stake.PaymentMethod{Current: "profit"},
				OnlinePlayers: intPointer(1_234),
			},
			"behind": {
				Earnings:      &stake.TeamEarnings{ProfitShare: -8_000_000, ExpectedProfit: -10_000_000},
				Balance:       &stake.TeamBalance{Position: -777_000_000, Carry: 500_000},
				PaymentMethod: &stake.PaymentMethod{Current: "turnover"},
				OnlinePlayers: intPointer(56),
			},
		},
	}

	view := model.View()
	for _, want := range []string{"Online", "1,234", "56", "Profit Share (10%)", "Expected (7.5%)", "Carry", "$12.00", "$10.00", "-$8.00", "-$10.00", "●", "selected payment model"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view does not contain %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"Standing", "$999.00", "-$777.00"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view unexpectedly contains %q:\n%s", unwanted, view)
		}
	}
}

func intPointer(value int) *int { return &value }

func TestFormatBalanceUsesPortalMicroUnits(t *testing.T) {
	if got := formatBalance(1_250_000); got != "$1.25" {
		t.Fatalf("formatBalance() = %q, want $1.25", got)
	}
	if got := formatBalance(-1_250_000); got != "-$1.25" {
		t.Fatalf("formatBalance() = %q, want -$1.25", got)
	}
}

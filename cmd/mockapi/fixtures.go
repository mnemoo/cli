package main

import (
	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
)

// demoUser is returned for any non-empty sid.
var demoUser = auth.User{
	ID:    "u_demo_mnemoo",
	Name:  "Mnemoo",
	Email: "mnemoo@stake.com",
	Image: "",
}

func ptrFloat(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// TUI formatting reference (internal/tui/games/games.go):
//     Profit  displayed as  raw / 1e7   → "$X,XXX.XX"
//     Turnover displayed as raw / 1e6   → "$XXX,XXX.XX"
//
// Target: profits $2-3K, turnovers ~$400K per game.
// ---------------------------------------------------------------------------

var teams = []api.TeamListItem{
	{
		Name: "Neon Labs", Slug: "neon-labs", TrustLevel: 4.82,
		Stats: &api.TeamStats{
			Day:   &api.StatPeriod{Count: 128_431, Turnover: 68_412_000_000, Profit: 4_112_800_000},
			Month: &api.StatPeriod{Count: 3_847_239, Turnover: 2_042_876_000_000, Profit: 121_407_000_000},
		},
	},
	{
		Name: "Aurora Studios", Slug: "aurora-studios", TrustLevel: 4.61,
		Stats: &api.TeamStats{
			Day:   &api.StatPeriod{Count: 84_120, Turnover: 37_882_000_000, Profit: 2_112_400_000},
			Month: &api.StatPeriod{Count: 2_410_882, Turnover: 1_132_092_000_000, Profit: 63_588_000_000},
		},
	},
	{
		Name: "Crimson Games", Slug: "crimson-games", TrustLevel: 4.12,
		Stats: &api.TeamStats{
			Day:   &api.StatPeriod{Count: 42_889, Turnover: 24_622_000_000, Profit: 1_071_200_000},
			Month: &api.StatPeriod{Count: 1_284_501, Turnover: 738_683_000_000, Profit: 32_134_000_000},
		},
	},
	{
		Name: "Indie Collab", Slug: "indie-collab", TrustLevel: 3.68,
		Stats: &api.TeamStats{
			Day:   &api.StatPeriod{Count: 18_241, Turnover: 4_685_000_000, Profit: 245_200_000},
			Month: &api.StatPeriod{Count: 542_881, Turnover: 140_554_000_000, Profit: 7_366_000_000},
		},
	},
}

// ---------------------------------------------------------------------------
// Games by team slug.
// ---------------------------------------------------------------------------

var gamesByTeam = map[string][]api.TeamGameCard{
	"neon-labs": {
		{
			Name: "Gods of Neon", Slug: "gods-of-neon", Rating: ptrFloat(4.73), Published: true, OnlinePlayers: 1_247,
			Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"},
			Stats: &api.TeamStats{
				Day:   &api.StatPeriod{Count: 28_431, Turnover: 16_240_000_000, Profit: 947_100_000},
				Month: &api.StatPeriod{Count: 873_221, Turnover: 487_221_000_000, Profit: 28_412_000_000},
			},
		},
		{
			Name: "Midnight Vault", Slug: "midnight-vault", Rating: ptrFloat(4.58), Published: true, OnlinePlayers: 832,
			Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"},
			Stats: &api.TeamStats{
				Day:   &api.StatPeriod{Count: 21_102, Turnover: 12_748_000_000, Profit: 704_300_000},
				Month: &api.StatPeriod{Count: 633_441, Turnover: 382_441_000_000, Profit: 21_128_000_000},
			},
		},
		{
			Name: "Thunder Empress", Slug: "thunder-empress", Rating: ptrFloat(4.91), Published: true, OnlinePlayers: 2_118,
			Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"},
			Stats: &api.TeamStats{
				Day:   &api.StatPeriod{Count: 34_882, Turnover: 20_429_000_000, Profit: 1_297_400_000},
				Month: &api.StatPeriod{Count: 1_038_112, Turnover: 612_882_000_000, Profit: 38_921_000_000},
			},
		},
		{
			Name: "Sakura Fortune X", Slug: "sakura-fortune-x", Rating: ptrFloat(4.44), Published: true, OnlinePlayers: 641,
			Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"},
			Stats: &api.TeamStats{
				Day:   &api.StatPeriod{Count: 17_412, Turnover: 10_607_000_000, Profit: 627_500_000},
				Month: &api.StatPeriod{Count: 518_220, Turnover: 318_220_000_000, Profit: 18_824_000_000},
			},
		},
		{
			Name: "Nebula Rush", Slug: "nebula-rush", Rating: ptrFloat(4.32), Published: true, OnlinePlayers: 389,
			Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"},
			Stats: &api.TeamStats{
				Day:   &api.StatPeriod{Count: 14_228, Turnover: 8_070_000_000, Profit: 470_700_000},
				Month: &api.StatPeriod{Count: 421_122, Turnover: 242_112_000_000, Profit: 14_120_000_000},
			},
		},
		{
			Name: "Cyber Samurai", Slug: "cyber-samurai", Rating: ptrFloat(4.12), Published: false,
			Approval: &api.GameApproval{Open: true, Locked: false, Column: "in_review"},
			Stats:    nil,
		},
		{
			Name: "Plasma Drift", Slug: "plasma-drift", Rating: nil, Published: false,
			Approval: &api.GameApproval{Open: true, Locked: false, Column: "draft"},
			Stats:    nil,
		},
		{
			Name: "Ghost Protocol", Slug: "ghost-protocol", Rating: ptrFloat(4.18), Published: false,
			Approval: &api.GameApproval{Open: true, Locked: false, Column: "in_review"},
			Stats:    nil,
		},
	},
	"aurora-studios": {
		{Name: "Arctic Reels", Slug: "arctic-reels", Rating: ptrFloat(4.55), Published: true, OnlinePlayers: 712, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 19_412, Turnover: 10_412_000_000, Profit: 627_300_000}, Month: &api.StatPeriod{Count: 582_110, Turnover: 312_110_000_000, Profit: 18_821_000_000}}},
		{Name: "Polar Heist", Slug: "polar-heist", Rating: ptrFloat(4.27), Published: true, OnlinePlayers: 445, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 14_882, Turnover: 8_141_000_000, Profit: 480_700_000}, Month: &api.StatPeriod{Count: 444_220, Turnover: 244_220_000_000, Profit: 14_422_000_000}}},
		{Name: "Aurora Rising", Slug: "aurora-rising", Rating: ptrFloat(4.71), Published: true, OnlinePlayers: 923, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 22_001, Turnover: 13_441_000_000, Profit: 740_200_000}, Month: &api.StatPeriod{Count: 659_881, Turnover: 402_881_000_000, Profit: 22_210_000_000}}},
		{Name: "Frostline", Slug: "frostline", Rating: ptrFloat(3.98), Published: true, OnlinePlayers: 218, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 9_112, Turnover: 5_888_000_000, Profit: 264_200_000}, Month: &api.StatPeriod{Count: 272_881, Turnover: 172_881_000_000, Profit: 8_135_000_000}}},
		{Name: "Borealis Bonus", Slug: "borealis-bonus", Rating: nil, Published: false, Approval: &api.GameApproval{Open: true, Locked: false, Column: "draft"}},
	},
	"crimson-games": {
		{Name: "Inferno Empress", Slug: "inferno-empress", Rating: ptrFloat(4.33), Published: true, OnlinePlayers: 334, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 11_888, Turnover: 8_563_000_000, Profit: 370_900_000}, Month: &api.StatPeriod{Count: 356_881, Turnover: 256_881_000_000, Profit: 11_128_000_000}}},
		{Name: "Crimson Tide", Slug: "crimson-tide", Rating: ptrFloat(4.01), Published: true, OnlinePlayers: 187, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 8_220, Turnover: 6_627_000_000, Profit: 262_700_000}, Month: &api.StatPeriod{Count: 246_812, Turnover: 198_812_000_000, Profit: 7_882_000_000}}},
		{Name: "Dragon Coin Deluxe", Slug: "dragon-coin-deluxe", Rating: ptrFloat(4.19), Published: true, OnlinePlayers: 421, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 13_441, Turnover: 9_432_000_000, Profit: 437_600_000}, Month: &api.StatPeriod{Count: 402_990, Turnover: 282_990_000_000, Profit: 13_124_000_000}}},
		{Name: "Lantern Nights", Slug: "lantern-nights", Rating: nil, Published: false, Approval: &api.GameApproval{Open: true, Locked: false, Column: "in_review"}},
	},
	"indie-collab": {
		{Name: "Forest Path", Slug: "forest-path", Rating: ptrFloat(3.82), Published: true, OnlinePlayers: 92, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 4_112, Turnover: 2_806_000_000, Profit: 147_400_000}, Month: &api.StatPeriod{Count: 124_112, Turnover: 84_112_000_000, Profit: 4_422_000_000}}},
		{Name: "Pixel Fortune", Slug: "pixel-fortune", Rating: ptrFloat(3.56), Published: true, OnlinePlayers: 54, Approval: &api.GameApproval{Open: false, Locked: true, Column: "approved"}, Stats: &api.TeamStats{Day: &api.StatPeriod{Count: 2_881, Turnover: 1_879_000_000, Profit: 97_800_000}, Month: &api.StatPeriod{Count: 86_442, Turnover: 56_442_000_000, Profit: 2_944_000_000}}},
		{Name: "Sketch Lab", Slug: "sketch-lab", Rating: nil, Published: false, Approval: &api.GameApproval{Open: true, Locked: false, Column: "draft"}},
	},
}

// ---------------------------------------------------------------------------
// Balances per team.
// ---------------------------------------------------------------------------

// Position displayed as raw / 1e6. Standings = current month profit.
// Must match sum of monthly game profits per team.
// Neon Labs:      $2,841+$2,112+$3,892+$1,882+$1,412 = ~$12,139
// Aurora Studios: $1,882+$1,442+$2,221+$812           = ~$6,357
// Crimson Games:  $1,112+$788+$1,312                  = ~$3,212
// Indie Collab:   $442+$294                            = ~$736
var balances = map[string]api.TeamBalance{
	"neon-labs":      {Position: 12_139_000_000, Carry: 1_441_000_000},
	"aurora-studios": {Position: 6_357_000_000, Carry: 812_000_000},
	"crimson-games":  {Position: 3_212_000_000, Carry: 437_000_000},
	"indie-collab":   {Position: 736_000_000, Carry: 98_000_000},
}

// ---------------------------------------------------------------------------
// Per-game stats (GET /teams/{team}/games/{game}/stats).
// Keyed by "team/game".
// ---------------------------------------------------------------------------

// RTP values as fractions (0.9642 = 96.42%) — TUI multiplies by 100.
// Turnover/Profit use the same /1e7 formatting as the games list.
var gameStats = map[string]api.GameStatsByModeResponse{
	"neon-labs/gods-of-neon": {
		Name: "Gods of Neon", Slug: "gods-of-neon",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 823_412, Turnover: 82_341_200_000, Profit: 2_982_050_000, Cost: 1.0, ExpectedRtp: 0.9642, EffectiveRtp: 0.9638, NormalizedRtp: 0.9640, Rtp: 0.9638},
			{Mode: "freespins", Count: 18_224, Turnover: 182_240_000_000, Profit: 6_888_241_000, Cost: 100.0, ExpectedRtp: 0.9621, EffectiveRtp: 0.9614, NormalizedRtp: 0.9618, Rtp: 0.9614},
			{Mode: "bonus_buy", Count: 11_442, Turnover: 228_840_000_000, Profit: 8_412_209_000, Cost: 200.0, ExpectedRtp: 0.9631, EffectiveRtp: 0.9628, NormalizedRtp: 0.9630, Rtp: 0.9628},
			{Mode: "max_buy", Count: 4_228, Turnover: 211_400_000_000, Profit: 7_811_044_000, Cost: 500.0, ExpectedRtp: 0.9644, EffectiveRtp: 0.9641, NormalizedRtp: 0.9642, Rtp: 0.9641},
		},
	},
	"neon-labs/midnight-vault": {
		Name: "Midnight Vault", Slug: "midnight-vault",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 633_441, Turnover: 63_344_100_000, Profit: 2_288_112_000, Cost: 1.0, ExpectedRtp: 0.9638, EffectiveRtp: 0.9632, Rtp: 0.9632},
			{Mode: "freespins", Count: 12_881, Turnover: 128_810_000_000, Profit: 4_821_108_000, Cost: 100.0, ExpectedRtp: 0.9618, EffectiveRtp: 0.9611, Rtp: 0.9611},
			{Mode: "bonus_buy", Count: 8_112, Turnover: 162_240_000_000, Profit: 6_244_022_000, Cost: 200.0, ExpectedRtp: 0.9622, EffectiveRtp: 0.9618, Rtp: 0.9618},
		},
	},
	"neon-labs/thunder-empress": {
		Name: "Thunder Empress", Slug: "thunder-empress",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 1_038_112, Turnover: 103_811_200_000, Profit: 3_841_109_000, Cost: 1.0, ExpectedRtp: 0.9670, EffectiveRtp: 0.9665, Rtp: 0.9665},
			{Mode: "freespins", Count: 28_112, Turnover: 281_120_000_000, Profit: 10_888_210_000, Cost: 100.0, ExpectedRtp: 0.9652, EffectiveRtp: 0.9648, Rtp: 0.9648},
			{Mode: "bonus_buy", Count: 16_441, Turnover: 328_820_000_000, Profit: 12_844_100_000, Cost: 200.0, ExpectedRtp: 0.9659, EffectiveRtp: 0.9655, Rtp: 0.9655},
			{Mode: "max_buy", Count: 7_114, Turnover: 355_700_000_000, Profit: 13_488_912_000, Cost: 500.0, ExpectedRtp: 0.9668, EffectiveRtp: 0.9663, Rtp: 0.9663},
		},
	},
	"neon-labs/sakura-fortune-x": {
		Name: "Sakura Fortune X", Slug: "sakura-fortune-x",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 518_220, Turnover: 51_822_000_000, Profit: 1_844_118_000, Cost: 1.0, ExpectedRtp: 0.9644, EffectiveRtp: 0.9638, Rtp: 0.9638},
			{Mode: "freespins", Count: 10_224, Turnover: 102_240_000_000, Profit: 3_811_209_000, Cost: 100.0, ExpectedRtp: 0.9628, EffectiveRtp: 0.9620, Rtp: 0.9620},
		},
	},
	"neon-labs/nebula-rush": {
		Name: "Nebula Rush", Slug: "nebula-rush",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 421_122, Turnover: 42_112_200_000, Profit: 1_488_211_000, Cost: 1.0, ExpectedRtp: 0.9650, EffectiveRtp: 0.9643, Rtp: 0.9643},
			{Mode: "freespins", Count: 8_441, Turnover: 84_410_000_000, Profit: 3_144_100_000, Cost: 100.0, ExpectedRtp: 0.9631, EffectiveRtp: 0.9627, Rtp: 0.9627},
			{Mode: "bonus_buy", Count: 5_112, Turnover: 102_240_000_000, Profit: 3_899_241_000, Cost: 200.0, ExpectedRtp: 0.9638, EffectiveRtp: 0.9632, Rtp: 0.9632},
		},
	},
	"aurora-studios/aurora-rising": {
		Name: "Aurora Rising", Slug: "aurora-rising",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 659_881, Turnover: 65_988_100_000, Profit: 2_444_112_000, Cost: 1.0, ExpectedRtp: 0.9635, EffectiveRtp: 0.9630, Rtp: 0.9630},
			{Mode: "freespins", Count: 13_441, Turnover: 134_410_000_000, Profit: 5_011_200_000, Cost: 100.0, ExpectedRtp: 0.9620, EffectiveRtp: 0.9614, Rtp: 0.9614},
		},
	},
	"aurora-studios/arctic-reels": {
		Name: "Arctic Reels", Slug: "arctic-reels",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 582_110, Turnover: 58_211_000_000, Profit: 2_144_112_000, Cost: 1.0, ExpectedRtp: 0.9628, EffectiveRtp: 0.9622, Rtp: 0.9622},
			{Mode: "freespins", Count: 11_882, Turnover: 118_820_000_000, Profit: 4_411_200_000, Cost: 100.0, ExpectedRtp: 0.9618, EffectiveRtp: 0.9611, Rtp: 0.9611},
		},
	},
	"crimson-games/inferno-empress": {
		Name: "Inferno Empress", Slug: "inferno-empress",
		Stats: []api.GameModeStat{
			{Mode: "base", Count: 356_881, Turnover: 35_688_100_000, Profit: 1_288_200_000, Cost: 1.0, ExpectedRtp: 0.9641, EffectiveRtp: 0.9633, Rtp: 0.9633},
			{Mode: "bonus_buy", Count: 4_112, Turnover: 82_240_000_000, Profit: 3_044_112_000, Cost: 200.0, ExpectedRtp: 0.9630, EffectiveRtp: 0.9625, Rtp: 0.9625},
		},
	},
}

// ---------------------------------------------------------------------------
// Per-game version history (GET /teams/{team}/games/{game}/versions).
// ---------------------------------------------------------------------------

// Timestamps in milliseconds (TUI divides by 1000 before formatting).
// Dates spread across late 2025 → early 2026 for a realistic version history.
var gameVersions = map[string][]api.GameVersionHistoryItem{
	"neon-labs/gods-of-neon": {
		{Type: "math", Version: 7, Created: 1743292800_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}},  // 2025-03-30
		{Type: "front", Version: 5, Created: 1743033600_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-27
		{Type: "math", Version: 6, Created: 1740441600_000},                                                                 // 2025-02-25
		{Type: "front", Version: 4, Created: 1738800000_000},                                                                // 2025-02-06
		{Type: "math", Version: 5, Created: 1735689600_000},                                                                 // 2025-01-01
	},
	"neon-labs/midnight-vault": {
		{Type: "math", Version: 4, Created: 1742860800_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}},  // 2025-03-25
		{Type: "front", Version: 3, Created: 1742774400_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-24
		{Type: "math", Version: 3, Created: 1739404800_000},                                                                 // 2025-02-13
	},
	"neon-labs/thunder-empress": {
		{Type: "math", Version: 11, Created: 1743379200_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-31
		{Type: "front", Version: 8, Created: 1743292800_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-30
		{Type: "math", Version: 10, Created: 1740528000_000},                                                                // 2025-02-26
		{Type: "math", Version: 9, Created: 1738022400_000},                                                                 // 2025-01-28
		{Type: "front", Version: 7, Created: 1736812800_000},                                                                // 2025-01-14
		{Type: "math", Version: 8, Created: 1735084800_000},                                                                 // 2024-12-25
	},
	"neon-labs/sakura-fortune-x": {
		{Type: "math", Version: 6, Created: 1742947200_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}},  // 2025-03-26
		{Type: "front", Version: 4, Created: 1742860800_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-25
	},
	"neon-labs/nebula-rush": {
		{Type: "math", Version: 5, Created: 1742688000_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}},  // 2025-03-23
		{Type: "front", Version: 4, Created: 1742601600_000, Approved: []api.VersionApproved{{Slug: "prod", Active: true}}}, // 2025-03-22
		{Type: "math", Version: 4, Created: 1740355200_000},                                                                 // 2025-02-24
	},
	"neon-labs/cyber-samurai":  {},
	"neon-labs/plasma-drift":   {},
	"neon-labs/ghost-protocol": {},
}

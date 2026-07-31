package api

import "github.com/mnemoo/cli/internal/stake"

// Compatibility aliases keep the transport API stable while domain ownership
// lives in internal/stake. New application code should use stake directly.
type TeamListItem = stake.Team
type TeamStats = stake.TeamStats
type StatPeriod = stake.StatPeriod
type TeamGameCard = stake.Game
type GameApproval = stake.GameApproval
type TeamGameDetail = stake.GameDetail
type VersionApproved = stake.VersionApproval
type GameVersionHistoryItem = stake.GameVersion
type TeamBalance = stake.TeamBalance
type TeamOperatorStat = stake.TeamOperatorStat
type PaymentMethod = stake.PaymentMethod
type GameModeStat = stake.GameModeStat
type GameStatsByModeResponse = stake.GameStats
type StatsGraphResponse = stake.StatsGraph

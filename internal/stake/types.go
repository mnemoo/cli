// Package stake contains the domain models shared by every stakecli adapter.
//
// These types deliberately do not know about HTTP, terminals, or MCP. Keeping
// them here prevents presentation layers from depending on the Stake Engine
// transport implementation.
package stake

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Image string `json:"image"`
}

type Team struct {
	Name       string     `json:"name"`
	Slug       string     `json:"slug"`
	Image      *string    `json:"image"`
	TrustLevel float64    `json:"trustLevel"`
	Stats      *TeamStats `json:"stats"`
}

type TeamStats struct {
	Day   *StatPeriod `json:"day"`
	Month *StatPeriod `json:"month"`
}

type StatPeriod struct {
	Count    int64   `json:"count"`
	Turnover float64 `json:"turnover"`
	Profit   float64 `json:"profit"`
}

type Game struct {
	Name          string        `json:"name"`
	Slug          string        `json:"slug"`
	Rating        *float64      `json:"rating"`
	Image         *string       `json:"image"`
	Published     bool          `json:"published"`
	Approval      *GameApproval `json:"approval"`
	Stats         *TeamStats    `json:"stats"`
	OnlinePlayers int           `json:"onlinePlayers"`
}

type GameApproval struct {
	Open   bool   `json:"open"`
	Locked bool   `json:"locked"`
	Column string `json:"column"`
}

type GameDetail struct {
	Name          string        `json:"name"`
	Slug          string        `json:"slug"`
	Image         *string       `json:"image"`
	Rating        *float64      `json:"rating"`
	Approval      *GameApproval `json:"approval"`
	OnlinePlayers int           `json:"onlinePlayers"`
}

type VersionApproval struct {
	Slug   string `json:"slug"`
	Active bool   `json:"active"`
}

type GameVersion struct {
	Type     string            `json:"type"`
	Created  float64           `json:"created"`
	Version  int               `json:"version"`
	Approved []VersionApproval `json:"approved"`
}

type TeamBalance struct {
	Position       float64 `json:"position"`
	ExpectedProfit float64 `json:"expectedProfit"`
	Carry          float64 `json:"carry"`
}

type TeamOperatorStat struct {
	BetCount       int64   `json:"betCount"`
	Turnover       float64 `json:"turnover"`
	Profit         float64 `json:"profit"`
	ExpectedProfit float64 `json:"expectedProfit"`
	UniqueUsers    int64   `json:"uniqueUsers"`
	Operator       string  `json:"operator"`
}

type TeamEarnings struct {
	ProfitShare    float64 `json:"profitShare"`
	ExpectedProfit float64 `json:"expectedProfit"`
}

type PaymentMethod struct {
	Current string `json:"current"`
	Next    string `json:"next"`
}

type GameModeStat struct {
	Mode           string  `json:"mode"`
	Count          int64   `json:"count"`
	Turnover       float64 `json:"turnover"`
	Profit         float64 `json:"profit"`
	ExpectedReturn float64 `json:"expectedReturn"`
	EffectiveRTP   float64 `json:"effectiveRtp"`
	NormalizedRTP  float64 `json:"normalizedRtp"`
	Cost           float64 `json:"cost"`
	RTP            float64 `json:"rtp"`
}

type GameStats struct {
	Name  string         `json:"name"`
	Slug  string         `json:"slug"`
	Image string         `json:"image"`
	Stats []GameModeStat `json:"stats"`
}

type StatsGraph struct {
	Buckets  []string  `json:"buckets"`
	Profit   []float64 `json:"profit"`
	Turnover []float64 `json:"turnover"`
	Count    []float64 `json:"count"`
}

type ScratchObject struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

type PublishResult struct {
	Version int     `json:"version"`
	Changed bool    `json:"changed"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	File    *string `json:"file,omitempty"`
	Mode    *string `json:"mode,omitempty"`
}

func (r *PublishResult) IsError() bool {
	return r.Code != ""
}

func (r *PublishResult) Error() string {
	s := r.Message
	if r.File != nil {
		s += " (file: " + *r.File + ")"
	}
	if r.Mode != nil {
		s += " (mode: " + *r.Mode + ")"
	}
	return s
}

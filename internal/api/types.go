package api

type TeamListItem struct {
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

type TeamGameCard struct {
	Name      string        `json:"name"`
	Slug      string        `json:"slug"`
	Rating    *float64      `json:"rating"`
	Image     *string       `json:"image"`
	Published bool          `json:"published"`
	Approval  *GameApproval `json:"approval"`
	Stats     *TeamStats    `json:"stats"`
}

type GameApproval struct {
	Open   bool   `json:"open"`
	Locked bool   `json:"locked"`
	Column string `json:"column"`
}

type TeamGameDetail struct {
	Name     string        `json:"name"`
	Slug     string        `json:"slug"`
	Image    *string       `json:"image"`
	Rating   *float64      `json:"rating"`
	Approval *GameApproval `json:"approval"`
}

type VersionApproved struct {
	Slug   string `json:"slug"`
	Active bool   `json:"active"`
}

type GameVersionHistoryItem struct {
	Type     string            `json:"type"`
	Created  float64           `json:"created"`
	Version  int               `json:"version"`
	Approved []VersionApproved `json:"approved"`
}

type TeamBalance struct {
	Position float64 `json:"position"`
	Carry    float64 `json:"carry"`
}

package stake

import "strings"

// ApprovalUser is the author attached to an approval timeline event.
type ApprovalUser struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Image string `json:"image,omitempty"`
	Staff bool   `json:"staff,omitempty"`
}

type ApprovalOperator struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// ApprovalEventData is the union of the data objects used by all currently
// known Stake Engine approval events. Pointer fields preserve the distinction
// between a missing field and an explicit zero/false value.
type ApprovalEventData struct {
	Comment  *string           `json:"comment,omitempty"`
	Images   []string          `json:"images,omitempty"`
	Version  *int              `json:"version,omitempty"`
	Kind     string            `json:"type,omitempty"`
	Operator *ApprovalOperator `json:"operator,omitempty"`
	Rating   *int              `json:"rating,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Name     string            `json:"name,omitempty"`
	Image    string            `json:"image,omitempty"`
	ID       *int64            `json:"id,omitempty"`
	Text     string            `json:"text,omitempty"`
	Open     *bool             `json:"open,omitempty"`
	Locked   *bool             `json:"locked,omitempty"`
}

type ApprovalEvent struct {
	ID          int64             `json:"id"`
	Time        int64             `json:"time"`
	User        ApprovalUser      `json:"user"`
	Type        string            `json:"type"`
	HasFeedback bool              `json:"hasFeedback,omitempty"`
	Data        ApprovalEventData `json:"data"`
}

// IsMessage reports whether the event carries human-authored conversation
// content. System-only timeline events remain visible but do not contribute to
// read/unread message counters.
func (e ApprovalEvent) IsMessage() bool {
	switch e.Type {
	case "comment", "private_comment":
		return true
	case "approval", "status_change":
		return e.Data.Comment != nil && strings.TrimSpace(*e.Data.Comment) != ""
	default:
		return false
	}
}

func (e ApprovalEvent) IsPrivate() bool { return e.Type == "private_comment" }

type ApprovalRatingSubmission struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Email   string   `json:"email,omitempty"`
	Image   string   `json:"image,omitempty"`
	Staff   bool     `json:"staff,omitempty"`
	Rating  *int     `json:"rating"`
	Comment string   `json:"comment,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type ApprovalGuidelineProgress struct {
	Total      int     `json:"total"`
	Checked    int     `json:"checked"`
	Percentage float64 `json:"percentage"`
}

type ApprovalRatingProgress struct {
	Count         int                        `json:"count"`
	Required      int                        `json:"required"`
	Complete      bool                       `json:"complete"`
	AverageRating float64                    `json:"averageRating"`
	Submissions   []ApprovalRatingSubmission `json:"submissions"`
}

// ApprovalConversation is the complete approval record returned by
// GET /teams/{team}/approvals/{game}.
type ApprovalConversation struct {
	Updated           int64                      `json:"updated"`
	Created           int64                      `json:"created"`
	LastAgentResponse *int64                     `json:"lastAgentResponse,omitempty"`
	EventCount        int                        `json:"eventCount"`
	MathVersion       int                        `json:"mathVersion"`
	FrontVersion      int                        `json:"frontVersion"`
	Open              bool                       `json:"open"`
	Locked            bool                       `json:"locked"`
	RateableAt        *int64                     `json:"rateableAt,omitempty"`
	Game              ApprovalOperator           `json:"game"`
	Team              Team                       `json:"team"`
	Approved          []string                   `json:"approved,omitempty"`
	IsLive            bool                       `json:"isLive,omitempty"`
	GuidelineProgress *ApprovalGuidelineProgress `json:"guidelineProgress,omitempty"`
	RatingProgress    *ApprovalRatingProgress    `json:"ratingProgress,omitempty"`
	Events            []ApprovalEvent            `json:"events"`
}

// ApprovalVersionUpdate describes the outcome of synchronizing one approval
// asset version with a version that was actually published.
type ApprovalVersionUpdate struct {
	Type            string `json:"type"`
	PreviousVersion int    `json:"previousVersion"`
	TargetVersion   int    `json:"targetVersion"`
	Open            bool   `json:"open"`
	Updated         bool   `json:"updated"`
	Warning         string `json:"warning,omitempty"`
}

type MessageStats struct {
	Total  int `json:"total"`
	Read   int `json:"read"`
	Unread int `json:"unread"`
}

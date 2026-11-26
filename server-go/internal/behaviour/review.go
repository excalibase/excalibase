package behaviour

import "time"

// Review statuses.
const (
	StatusOpen      = "open"
	StatusDismissed = "dismissed"
	StatusActioned  = "actioned"
)

// Rules are how repeated findings and dismissals behave.
type Rules struct {
	MuteDays     int
	GrowthFactor float64
}

// Outcome says what recording a finding did.
type Outcome string

const (
	OutcomeOpened     Outcome = "opened"
	OutcomeUpdated    Outcome = "updated"
	OutcomeSuppressed Outcome = "suppressed"
)

// Review is one stored finding: what a person looks at.
type Review struct {
	ID int64
	Subject
	Signal    string
	Status    string
	FirstSeen time.Time
	LastSeen  time.Time
	// Peak is the largest magnitude seen while the review was open.
	Peak     float64
	Evidence map[string]float64

	DismissedAt   *time.Time
	DismissedBy   string
	DismissedPeak float64
	MuteUntil     *time.Time
}

// AppVolume is one app's bytes over the last 24 hours and 30 days.
type AppVolume struct {
	ProjectID, AppID string
	Totals
}

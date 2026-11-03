package domain

import (
	"errors"
	"time"
)

// Log drain states (EXC-658). A drain the platform disabled after repeated
// failures keeps its last error; one an Admin disabled has none.
const (
	LogDrainActive   = "active"
	LogDrainFailing  = "failing"
	LogDrainDisabled = "disabled"
)

var (
	ErrLogDrainNotFound = errors.New("log drain not found")
	ErrLogDrainLimit    = errors.New("the project has reached its log drain limit")
)

// LogDrain forwards one project's logs to a Datadog site or an HTTPS
// endpoint. Its key and full endpoint live in the vault; URL is scheme and
// host only. CursorNS is the Loki timestamp of the last line delivered;
// LeaseTo and LeaseLines are the batch out with the forwarder.
type LogDrain struct {
	ID                  string
	ProjectID           string
	Name                string
	Kind                string
	DatadogSite         string
	URL                 string
	HeaderName          string
	Enabled             bool
	Status              string
	LastError           string
	ConsecutiveFailures int
	CursorNS            int64
	LeaseTo             int64
	LeaseLines          int
	NextAttemptAt       time.Time
	LastSuccessAt       *time.Time
	SentLines           int64
	DroppedLines        int64
	CreatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// LogDrainAdvance moves a drain's cursor From → To, only if it still sits at
// From. Delivered marks a batch the destination took, which clears failures
// and must match the lease.
type LogDrainAdvance struct {
	ID        string
	From      int64
	To        int64
	Sent      int64
	Dropped   int64
	Delivered bool
	Next      time.Time
}

// LogDrainLease hands the batch From → To to the forwarder until Until, only
// if the drain is still due at Now and sits at From.
type LogDrainLease struct {
	ID    string
	From  int64
	To    int64
	Lines int
	Until time.Time
	Now   time.Time
}

// LogDrainFailure records a batch that was not delivered, only if the drain
// still sits at From (and, when LeaseTo is set, holds that lease). Disable
// turns the drain off with the error kept.
type LogDrainFailure struct {
	ID      string
	From    int64
	LeaseTo int64
	Error   string
	Next    time.Time
	Disable bool
}

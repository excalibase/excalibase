package apphost

import (
	"errors"
	"time"
)

// ErrDeployNotFound marks a deploy id that does not belong to the given
// (projectID, appID) — whether it belongs to another app, another project, or
// nothing at all.
var ErrDeployNotFound = errors.New("deploy not found")

type DeployStore interface {
	// Create supersedes any earlier pending/rolling deploy of the same app.
	Create(deploy *Deploy) error
	// UpdateStatus applies only while the row is still pending/rolling, so a
	// late write loses instead of flipping a terminal status back over.
	UpdateStatus(id, status, failureReason string, finishedAt *time.Time) error
	// Finish records a deploy's outcome and the app status it observed in one
	// write, and only while the deploy is still pending/rolling: a superseded
	// deploy never speaks for the app. An empty appStatus leaves the app as it is.
	Finish(id, status, failureReason string, finishedAt time.Time, appStatus string) error
	// ListUnfinished returns every pending/rolling deploy, across projects.
	ListUnfinished() ([]*Deploy, error)
	ListByApp(projectID, appID string, limit int) ([]*Deploy, error)
	GetLatest(projectID, appID string) (*Deploy, error)
	// Get returns nil (no error) when id names no deploy scoped to
	// (projectID, appID).
	Get(projectID, appID, id string) (*Deploy, error)
}

var _ DeployStore = (*PostgresDeployStore)(nil)

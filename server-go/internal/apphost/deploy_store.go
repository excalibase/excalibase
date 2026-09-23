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
	ListByApp(projectID, appID string, limit int) ([]*Deploy, error)
	GetLatest(projectID, appID string) (*Deploy, error)
	// Get returns nil (no error) when id names no deploy scoped to
	// (projectID, appID).
	Get(projectID, appID, id string) (*Deploy, error)
}

var _ DeployStore = (*PostgresDeployStore)(nil)

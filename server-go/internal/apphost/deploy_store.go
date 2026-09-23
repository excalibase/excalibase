package apphost

import "time"

type DeployStore interface {
	// Create supersedes any earlier pending/rolling deploy of the same app.
	Create(deploy *Deploy) error
	// UpdateStatus applies only while the row is still pending/rolling, so a
	// late write loses instead of flipping a terminal status back over.
	UpdateStatus(id, status, failureReason string, finishedAt *time.Time) error
	ListByApp(projectID, appID string, limit int) ([]*Deploy, error)
	GetLatest(projectID, appID string) (*Deploy, error)
}

var _ DeployStore = (*PostgresDeployStore)(nil)

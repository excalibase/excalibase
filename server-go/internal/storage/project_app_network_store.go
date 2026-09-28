package storage

import "context"

// ProjectAppNetworkStore keeps whether a project's apps may reach each other
// (EXC-524). A project with no record is off.
type ProjectAppNetworkStore interface {
	GetAppPrivateNetwork(ctx context.Context, projectID string) (bool, error)
	SetAppPrivateNetwork(ctx context.Context, projectID string, enabled bool) error
}

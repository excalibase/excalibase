package apphost

import (
	"context"
	"errors"
	"fmt"
)

// Store is the persistence contract for customer applications.
var (
	// ErrAppNotFound is returned by writes that name an app the project does
	// not hold. Reads report absence as a nil app instead, so a caller never
	// has to tell "gone" from "failed" by parsing an error.
	ErrAppNotFound = errors.New("app not found")
	// ErrAppLimitReached is returned, as an AppLimitError, when the project
	// already holds as many apps as its plan allows. It is decided inside the
	// writing transaction, never by a read the caller made first.
	ErrAppLimitReached = errors.New("the project holds as many apps as its plan allows")
	// ErrAppNameTaken is returned when the project already holds an app of
	// that name. Names are unique per project only.
	ErrAppNameTaken = errors.New("app name already used in this project")
	// ErrAppVersionConflict is returned when an update states a version the
	// stored app has moved past. Two developers editing the same app then
	// find out, instead of one of the two changes disappearing.
	ErrAppVersionConflict = errors.New("app was changed by someone else")
	// ErrAppStatusConflict is returned when the app is not in a status the
	// requested lifecycle step may start from.
	ErrAppStatusConflict = errors.New("the app is not in a state that allows this")
	// ErrAppBusy is returned when a pause, resume or deletion owns the app.
	ErrAppBusy = errors.New("the app is being paused, resumed or deleted")
)

// Store persists apps. Every method is scoped by project id: an app is only
// ever reachable through the project that owns it, so a caller holding one
// project's id can never read or write another's row.
type Store interface {
	// Create stores a new app, refusing when the project already holds
	// maxApps apps (its plan's limit, EXC-524) or the name is already used.
	Create(app *App, maxApps int) error
	// Get returns nil (no error) when the project holds no such app.
	Get(projectID, id string) (*App, error)
	// List returns the project's apps (empty slice, not nil, when none).
	List(projectID string) ([]*App, error)
	// Update replaces the stored record and bumps its version. expectedVersion
	// is the version the caller read: the write applies only while the stored
	// row still holds it, and otherwise reports ErrAppVersionConflict without
	// writing. Reports ErrAppNotFound when the project holds no such app.
	// The stored status is kept: only a finished deploy or a lifecycle
	// transition moves it. Refused with ErrAppBusy while the app is deleting.
	Update(app *App, expectedVersion int) error
	// Transition moves the status to `to` only from one of `from`, and
	// supersedes any unfinished deploy in the same write, so a rollout still
	// being watched can no longer speak for the app.
	Transition(projectID, id string, from []string, to string) (*App, error)
	// Delete removes the app and its deploy history, only once its status is
	// StatusDeleting: the row outlives the workload, never the other way round.
	Delete(projectID, id string) error
}

// Compile-time check.
var _ Store = (*PostgresAppStore)(nil)

// AppLimitError names the plan's limit the create ran into.
type AppLimitError struct{ Limit int }

func (e AppLimitError) Error() string {
	return fmt.Sprintf("the project already has %d apps, the most its plan allows; delete one or move the organisation to a larger plan", e.Limit)
}

func (e AppLimitError) Is(target error) bool { return target == ErrAppLimitReached }

// AppLimits answers how many apps a project may hold under its organisation's
// current plan (EXC-524). A downgrade never deletes apps; it refuses new ones.
type AppLimits interface {
	MaxApps(ctx context.Context, projectID string) (int, error)
}

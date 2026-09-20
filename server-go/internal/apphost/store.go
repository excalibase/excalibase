package apphost

import "errors"

// Store is the persistence contract for customer applications.
var (
	// ErrAppNotFound is returned by writes that name an app the project does
	// not hold. Reads report absence as a nil app instead, so a caller never
	// has to tell "gone" from "failed" by parsing an error.
	ErrAppNotFound = errors.New("app not found")
	// ErrAppLimitReached is returned when the project already holds as many
	// apps as it may (MaxAppsPerProject). It is decided inside the writing
	// transaction, never by a read the caller made first.
	ErrAppLimitReached = errors.New("project already has an app")
	// ErrAppNameTaken is returned when the project already holds an app of
	// that name. Names are unique per project only.
	ErrAppNameTaken = errors.New("app name already used in this project")
)

// Store persists apps. Every method is scoped by project id: an app is only
// ever reachable through the project that owns it, so a caller holding one
// project's id can never read or write another's row.
type Store interface {
	// Create stores a new app, refusing when the project's app limit is
	// already taken or the name is already used in the project.
	Create(app *App) error
	// Get returns nil (no error) when the project holds no such app.
	Get(projectID, id string) (*App, error)
	// List returns the project's apps (empty slice, not nil, when none).
	List(projectID string) ([]*App, error)
	// Update replaces the stored record and bumps its version, reporting
	// ErrAppNotFound when the project holds no such app.
	Update(app *App) error
	// Delete removes the app, reporting ErrAppNotFound when absent.
	Delete(projectID, id string) error
}

// Compile-time check.
var _ Store = (*PostgresAppStore)(nil)

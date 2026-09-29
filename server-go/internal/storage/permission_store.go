package storage

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Errors a PermissionStore reports; handlers map them to 404, 409 or 400.
var (
	ErrPermissionNotFound         = errors.New("permission not found")
	ErrFunctionAlreadyTracked     = errors.New("function is already tracked")
	ErrFunctionNotTracked         = errors.New("function is not tracked")
	ErrFunctionPermissionNotFound = errors.New("function permission not found")
)

// PermissionStore persists a project's API permissions (EXC-370): table
// permissions, tracked functions and function permissions. Every write bumps
// the project's permission version in the same transaction.
type PermissionStore interface {
	// Document returns the project's whole permission set; slices are never nil.
	Document(ctx context.Context, projectID string) (*domain.PermissionDocument, error)
	// PutPermission creates or replaces one permission; created reports which.
	PutPermission(ctx context.Context, p domain.TablePermission) (created bool, err error)
	DeletePermission(ctx context.Context, projectID, table, role, operation string) error
	TrackFunction(ctx context.Context, fn domain.TrackedFunction) error
	// UntrackFunction removes a tracked function and its function permissions.
	UntrackFunction(ctx context.Context, projectID, function string) error
	PutFunctionPermission(ctx context.Context, projectID, function, role string) error
	DeleteFunctionPermission(ctx context.Context, projectID, function, role string) error

	// LegacyPending lists projects that hold table grants, row policies or
	// column policies not yet folded into permissions.
	LegacyPending(ctx context.Context) ([]string, error)
	// ImportLegacy writes a project's folded permissions without replacing
	// any that already exist, and marks the project migrated, atomically.
	ImportLegacy(ctx context.Context, projectID string, set domain.LegacyPermissionImport) error
}

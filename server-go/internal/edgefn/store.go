package edgefn

// Store is the persistence contract for edge functions. Two implementations:
// FunctionStore (filesystem, self-hosted/dev) and PostgresFunctionStore (the
// platform store used in cloud mode). Cloud deployments must use the Postgres
// one — the filesystem path is an emptyDir in the AIO chart, so pod restarts,
// reschedules, and scale-to-zero would destroy tenant-authored code (EXC-333).
type Store interface {
	// Save validates, assigns/bumps Version, and persists the function.
	Save(fn *Function) error
	// Get returns nil (no error) when the function does not exist.
	Get(projectID, id string) (*Function, error)
	// List returns every function for a project (empty slice, not nil, when none).
	List(projectID string) ([]*Function, error)
	// Delete is a no-op when the function does not exist.
	Delete(projectID, id string) error
}

// SharedFileStore holds project-level modules shared across a project's
// functions — Supabase's `_shared/` convention (EXC-334). They are seeded into
// the bundler's virtual-file map so `../_shared/x.ts` resolves without copying
// the source into every function.
type SharedFileStore interface {
	// SharedFiles returns the project's shared modules (empty slice when none).
	SharedFiles(projectID string) ([]File, error)
	// PutSharedFile creates or replaces one shared module.
	PutSharedFile(projectID string, file File) error
	// DeleteSharedFile removes one shared module; no-op when absent.
	DeleteSharedFile(projectID, path string) error
}

// Compile-time checks.
var (
	_ Store = (*FunctionStore)(nil)
	_ Store = (*PostgresFunctionStore)(nil)
)

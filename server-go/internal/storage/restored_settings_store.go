package storage

import "context"

// RestoredSettingsStore carries a project's platform-side API settings onto a
// copy restored from its backups (EXC-639). The copy's database arrives with
// the source's tables and functions, but who may reach them through the API
// lives in the platform database, keyed by project id.
type RestoredSettingsStore interface {
	// CopyRestoredSettings copies sourceID's table and function permissions,
	// tracked functions, CORS origins (except those an app added) and auth
	// settings onto targetID, all or nothing.
	CopyRestoredSettings(ctx context.Context, sourceID, targetID string) error
	// DisableMFA turns the copy's MFA switch off when its key could not be
	// carried, so the copy never claims a key auth cannot read.
	DisableMFA(ctx context.Context, projectID string) error
}

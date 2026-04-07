package storage

import "io"

// PlatformStore is the combined interface implemented by both SQLite and Postgres stores.
// It provides all storage capabilities needed by the platform.
type PlatformStore interface {
	InstanceStore
	UserStore
	TokenStore
	OrgStore
	io.Closer
}

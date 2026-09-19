package storagesvc

import (
	"errors"
	"fmt"
)

// Sentinels the HTTP layer maps onto a status code. Everything else coming
// out of this package is either a *ValidationError — a message about the
// caller's own input, safe to echo back — or an internal failure, which the
// handler logs and answers with a fixed message.
var (
	ErrBucketNotFound = errors.New("bucket not found")
	ErrObjectNotFound = errors.New("object not found")
	ErrBucketExists   = errors.New("bucket already exists")
	// ErrBucketDeleting guards a bucket whose bytes are being purged: its
	// catalogue rows may already outlive their objects, so it accepts no
	// new uploads until the delete finishes.
	ErrBucketDeleting = errors.New("bucket is being deleted")
	// errObjectStoreUnset is a wiring fault, never a caller mistake.
	errObjectStoreUnset = errors.New("object store not configured")
)

// ValidationError carries a message describing the caller's input. It never
// contains platform internals, so handlers return it verbatim.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalidf(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

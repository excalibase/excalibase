package storage

import (
	"context"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrDBEndpointPortsExhausted is returned when every port in the configured
// window is either held by a project or still in quarantine. It is a clear
// refusal on purpose: the alternative — reissuing a port whose quarantine has
// not expired — points a stranger's still-dialling clients at this tenant's
// database, and no amount of capacity pressure makes that acceptable.
var ErrDBEndpointPortsExhausted = errors.New("no public database port is available")

// DatabaseEndpointStore persists a project's public database endpoint
// setting and owns the port allocator behind it (EXC-410).
//
// Allocation is atomic: the port is chosen and written inside one
// transaction, under an advisory lock on the port space, and a unique
// constraint backs it up — never read-then-write. It is also random within
// the window, so nobody can infer the platform's tenant count or a project's
// signup order from the number they were given.
//
// Release puts the port in quarantine rather than straight back in the pool.
// The quarantine window is enforced here, in the allocator, not by the
// callers' good behaviour.
type DatabaseEndpointStore interface {
	// GetDatabaseEndpoint returns the project's setting. A project that has
	// never touched it reads as domain.DefaultDBEndpoint — off, no port,
	// TLS required — so a missing row and an explicit "off" are the same
	// answer to a caller.
	GetDatabaseEndpoint(ctx context.Context, projectID string) (domain.DBEndpoint, error)

	// AllocateDatabaseEndpointPort takes a port for the project and returns
	// the endpoint holding it, still with PublicEnabled false: the setting
	// is only flipped once the Service is observed to exist. A project that
	// already holds a port keeps it, so the call is idempotent and a resume
	// comes back on the same number. Returns ErrDBEndpointPortsExhausted
	// when the window has nothing free.
	AllocateDatabaseEndpointPort(ctx context.Context, projectID string, window domain.PortRange, quarantine time.Duration) (domain.DBEndpoint, error)

	// SetDatabaseEndpointPublic records whether the project's Service
	// exists. It is written after the Kubernetes object has been observed
	// created or deleted, never before.
	SetDatabaseEndpointPublic(ctx context.Context, projectID string, public bool) (domain.DBEndpoint, error)

	// SetDatabaseEndpointRequireTLS records the project's TLS choice. It is
	// a setting of the project, not of the port, so it survives the
	// endpoint being turned off and on again.
	SetDatabaseEndpointRequireTLS(ctx context.Context, projectID string, requireTLS bool) (domain.DBEndpoint, error)

	// ReleaseDatabaseEndpointPort frees the project's port into quarantine
	// and leaves the endpoint off. It is idempotent: a project holding no
	// port is already released, which is what a retried teardown needs.
	ReleaseDatabaseEndpointPort(ctx context.Context, projectID string, releasedAt time.Time) error

	// DeleteDatabaseEndpoint removes the project's row entirely, after its
	// port has been released. Used by project teardown; the quarantine
	// entry outlives the row it came from.
	DeleteDatabaseEndpoint(ctx context.Context, projectID string) error
}

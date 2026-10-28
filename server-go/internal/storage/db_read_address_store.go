package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DatabaseReadAddressStore records whether a project's reader Service exists
// (EXC-666). The port itself comes from DatabaseEndpointStore's allocator
// under domain.DBEndpointRoleReader.
type DatabaseReadAddressStore interface {
	// SetDatabaseEndpointRolePublic records one holding's Service as
	// present or gone, written only after it was observed.
	SetDatabaseEndpointRolePublic(ctx context.Context, projectID string, role domain.DBEndpointRole, public bool) (domain.DBEndpoint, error)
}

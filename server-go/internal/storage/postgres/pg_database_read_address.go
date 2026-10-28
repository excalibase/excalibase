package postgres

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const setRolePublicSQL = `
INSERT INTO database_endpoints (project_id, role, public, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (project_id, role) DO UPDATE SET public = EXCLUDED.public, updated_at = NOW()
RETURNING project_id, role, port, public, require_tls`

// SetDatabaseEndpointRolePublic records whether one holding's Service exists.
// See storage.DatabaseReadAddressStore.
func (s *Store) SetDatabaseEndpointRolePublic(ctx context.Context, projectID string, role domain.DBEndpointRole, public bool) (domain.DBEndpoint, error) {
	if !role.Valid() {
		return domain.DBEndpoint{}, fmt.Errorf("write database endpoint public: unknown endpoint role %q", role)
	}
	endpoint, err := scanDBEndpoint(s.db.QueryRowContext(ctx, setRolePublicSQL, projectID, string(role), public))
	if err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("write database endpoint public: %w", err)
	}
	return endpoint, nil
}

var _ storage.DatabaseReadAddressStore = (*Store)(nil)

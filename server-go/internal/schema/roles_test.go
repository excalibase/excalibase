package schema

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// Refused before any statement is sent: a nil pool would panic otherwise.
func TestStudioRefusesEveryReservedRoleName(t *testing.T) {
	introspector := NewIntrospector()
	names := append(pgroles.ReservedNames(), "pg_monitor", "excalibase_anything", "documentdb_admin_role", "CDC_WATCHER")
	for _, name := range names {
		if err := introspector.CreateRole(context.Background(), nil, CreateRoleRequest{Name: name, Login: true}); !errors.Is(err, ErrProtectedRole) {
			t.Errorf("create %s: %v", name, err)
		}
		if err := introspector.DropRole(context.Background(), nil, name); !errors.Is(err, ErrProtectedRole) {
			t.Errorf("drop %s: %v", name, err)
		}
	}
}

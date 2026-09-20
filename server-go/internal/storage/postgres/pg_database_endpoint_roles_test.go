//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-409: a DocumentDB project holds two ports, and both come from the one
// allocator EXC-410 built. These tests are about the allocator continuing to
// be one allocator — same window, same uniqueness, same quarantine — now that
// a project can hold more than one port.

func endpointWindow(t *testing.T, min, max int) domain.PortRange {
	t.Helper()
	window, err := domain.NewPortRange(min, max)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	return window
}

// The two roles are separate holdings of the same project.
func TestEndpointRoles_AProjectHoldsAPortPerRole(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31000, 31099)

	postgres, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles001", domain.DBEndpointRolePostgres, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate postgres: %v", err)
	}
	mongo, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles001", domain.DBEndpointRoleMongo, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate mongo: %v", err)
	}

	if postgres.Port == 0 || mongo.Port == 0 {
		t.Fatalf("a role holds no port: postgres=%d mongo=%d", postgres.Port, mongo.Port)
	}
	if postgres.Port == mongo.Port {
		t.Errorf("both roles were given port %d", postgres.Port)
	}
	for _, port := range []int{postgres.Port, mongo.Port} {
		if !window.Contains(port) {
			t.Errorf("port %d is outside the allocation window", port)
		}
	}
}

// Idempotence per role: a retried enable or a resume comes back on the number
// the customer has saved, for each protocol.
func TestEndpointRoles_AllocatingTwiceKeepsEachRolesPort(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31100, 31199)

	for _, role := range []domain.DBEndpointRole{domain.DBEndpointRolePostgres, domain.DBEndpointRoleMongo} {
		first, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles002", role, window, time.Hour)
		if err != nil {
			t.Fatalf("%s: first allocate: %v", role, err)
		}
		second, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles002", role, window, time.Hour)
		if err != nil {
			t.Fatalf("%s: second allocate: %v", role, err)
		}
		if first.Port != second.Port {
			t.Errorf("%s: port moved from %d to %d", role, first.Port, second.Port)
		}
	}
}

// One port, one holder, across every project and every role. This is the
// property the whole scheme rests on, and a window of one port is the only
// way to assert it without depending on luck.
func TestEndpointRoles_OnePortIsNeverHeldTwice(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	single := endpointWindow(t, 31200, 31200)

	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles003", domain.DBEndpointRolePostgres, single, time.Hour); err != nil {
		t.Fatalf("allocate the only port: %v", err)
	}
	// The same project's other role cannot have it either.
	_, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles003", domain.DBEndpointRoleMongo, single, time.Hour)
	if err == nil {
		t.Fatal("the one free port was handed out twice to the same project")
	}
	// Nor another project's.
	_, err = store.AllocateDatabaseEndpointPort(ctx, "proj-roles004", domain.DBEndpointRolePostgres, single, time.Hour)
	if err == nil {
		t.Fatal("the one free port was handed out to a second project")
	}
}

// Releasing one role's port quarantines it and leaves the other role alone.
func TestEndpointRoles_ReleasingOneRoleQuarantinesOnlyItsPort(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// Three ports: one for each role, and one left over — so the allocation
	// after the release has somewhere to go that is not the quarantined port.
	window := endpointWindow(t, 31300, 31302)

	postgres, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles005", domain.DBEndpointRolePostgres, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate postgres: %v", err)
	}
	mongo, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles005", domain.DBEndpointRoleMongo, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate mongo: %v", err)
	}

	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-roles005", domain.DBEndpointRoleMongo, time.Now()); err != nil {
		t.Fatalf("release mongo: %v", err)
	}

	held, err := store.GetDatabaseEndpointForRole(ctx, "proj-roles005", domain.DBEndpointRolePostgres)
	if err != nil {
		t.Fatalf("read postgres role: %v", err)
	}
	if held.Port != postgres.Port {
		t.Errorf("the Postgres port changed when the Mongo one was released: %d", held.Port)
	}
	freed, err := store.GetDatabaseEndpointForRole(ctx, "proj-roles005", domain.DBEndpointRoleMongo)
	if err != nil {
		t.Fatalf("read mongo role: %v", err)
	}
	if freed.Port != 0 {
		t.Errorf("the Mongo role still holds port %d", freed.Port)
	}

	// The freed port is in quarantine, so the window's only other port is all
	// that is left to give out.
	next, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles006", domain.DBEndpointRolePostgres, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate after release: %v", err)
	}
	if next.Port == mongo.Port {
		t.Errorf("the released Mongo port %d was reissued inside its quarantine", mongo.Port)
	}
}

// A quarantine that has expired releases the port back to the pool, for a
// Mongo port exactly as for a Postgres one.
func TestEndpointRoles_AnExpiredQuarantineReleasesAMongoPort(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	single := endpointWindow(t, 31400, 31400)

	first, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles007", domain.DBEndpointRoleMongo, single, time.Hour)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-roles007", domain.DBEndpointRoleMongo, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("release: %v", err)
	}

	again, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles008", domain.DBEndpointRoleMongo, single, time.Hour)
	if err != nil {
		t.Fatalf("allocate after the quarantine expired: %v", err)
	}
	if again.Port != first.Port {
		t.Errorf("got port %d, want the released %d", again.Port, first.Port)
	}
}

// Deleting a project removes both of its rows, not only the Postgres one.
func TestEndpointRoles_DeletingAProjectRemovesEveryRole(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31500, 31599)

	for _, role := range []domain.DBEndpointRole{domain.DBEndpointRolePostgres, domain.DBEndpointRoleMongo} {
		if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles009", role, window, time.Hour); err != nil {
			t.Fatalf("%s: allocate: %v", role, err)
		}
	}
	if err := store.DeleteDatabaseEndpoint(ctx, "proj-roles009"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, role := range []domain.DBEndpointRole{domain.DBEndpointRolePostgres, domain.DBEndpointRoleMongo} {
		endpoint, err := store.GetDatabaseEndpointForRole(ctx, "proj-roles009", role)
		if err != nil {
			t.Fatalf("%s: read back: %v", role, err)
		}
		if endpoint.Port != 0 || endpoint.PublicEnabled {
			t.Errorf("%s: row survived the delete: %+v", role, endpoint)
		}
	}
}

// An unrecognised role is refused rather than written: a row nothing reads
// back is a port held forever by nobody.
func TestEndpointRoles_AnUnknownRoleIsRefused(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	_, err := store.AllocateDatabaseEndpointPort(ctx, "proj-roles010",
		domain.DBEndpointRole("redis"), endpointWindow(t, 31600, 31699), time.Hour)
	if err == nil {
		t.Fatal("an unknown role was allocated a port")
	}
	if !errorMentions(err, "role") {
		t.Errorf("the refusal does not say what was wrong: %v", err)
	}
}

func errorMentions(err error, what string) bool {
	return err != nil && len(err.Error()) > 0 && contains(err.Error(), what)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

var _ = storage.ErrDBEndpointPortsExhausted

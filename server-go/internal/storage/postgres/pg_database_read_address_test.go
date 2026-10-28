//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-666: the read address holds a port of its own from the one allocator,
// recorded public only once its Service is observed, quarantined on release.

func TestReadAddress_HoldsItsOwnPortBesideThePrimary(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31400, 31499)

	primary, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader001", domain.DBEndpointRolePostgres, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate primary: %v", err)
	}
	reader, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader001", domain.DBEndpointRoleReader, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate reader: %v", err)
	}
	if reader.Port == 0 || reader.Port == primary.Port {
		t.Fatalf("reader port %d, primary port %d", reader.Port, primary.Port)
	}
	if reader.PublicEnabled {
		t.Fatal("a freshly allocated reader port must not read as public")
	}
}

func TestReadAddress_PublicIsRecordedOnTheReaderRowOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31500, 31599)
	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader002", domain.DBEndpointRoleReader, window, time.Hour); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	got, err := store.SetDatabaseEndpointRolePublic(ctx, "proj-reader002", domain.DBEndpointRoleReader, true)
	if err != nil {
		t.Fatalf("set public: %v", err)
	}
	if !got.PublicEnabled || got.Role != domain.DBEndpointRoleReader {
		t.Fatalf("reader row = %+v", got)
	}
	primary, err := store.GetDatabaseEndpoint(ctx, "proj-reader002")
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	if primary.PublicEnabled {
		t.Fatal("setting the reader public must not open the primary port")
	}
}

func TestReadAddress_SetPublicRefusesAnUnknownRole(t *testing.T) {
	store := testStore(t)
	if _, err := store.SetDatabaseEndpointRolePublic(context.Background(), "proj-reader003", domain.DBEndpointRole("replica"), true); err == nil {
		t.Fatal("an unknown role was written")
	}
}

func TestReadAddress_ReleaseQuarantinesTheReaderPort(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31600, 31600)

	held, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader004", domain.DBEndpointRoleReader, window, time.Hour)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if _, err := store.SetDatabaseEndpointRolePublic(ctx, "proj-reader004", domain.DBEndpointRoleReader, true); err != nil {
		t.Fatalf("set public: %v", err)
	}
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-reader004", domain.DBEndpointRoleReader, time.Now()); err != nil {
		t.Fatalf("release: %v", err)
	}
	after, err := store.GetDatabaseEndpointForRole(ctx, "proj-reader004", domain.DBEndpointRoleReader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if after.Port != 0 || after.PublicEnabled {
		t.Fatalf("released reader still holds %+v", after)
	}
	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader005", domain.DBEndpointRolePostgres, window, time.Hour); err == nil {
		t.Fatalf("port %d was reissued inside its quarantine", held.Port)
	}
}

func TestReadAddress_TheTableRefusesAnUnknownRole(t *testing.T) {
	store := testStore(t)
	_, err := store.db.ExecContext(context.Background(),
		`INSERT INTO database_endpoints (project_id, role) VALUES ('proj-reader006', 'replica')`)
	if err == nil {
		t.Fatal("the role CHECK accepted an unknown role")
	}
}

func TestReadAddress_DeleteRemovesTheReaderRow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	window := endpointWindow(t, 31700, 31799)
	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-reader007", domain.DBEndpointRoleReader, window, time.Hour); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := store.DeleteDatabaseEndpoint(ctx, "proj-reader007"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	after, err := store.GetDatabaseEndpointForRole(ctx, "proj-reader007", domain.DBEndpointRoleReader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if after.Port != 0 {
		t.Fatalf("reader row survived the delete: %+v", after)
	}
}

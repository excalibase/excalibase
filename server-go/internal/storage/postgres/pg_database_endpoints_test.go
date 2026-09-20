//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testQuarantine = 30 * 24 * time.Hour

func testWindow(t *testing.T, lowest, highest int) domain.PortRange {
	t.Helper()
	window, err := domain.NewPortRange(lowest, highest)
	if err != nil {
		t.Fatalf("NewPortRange: %v", err)
	}
	return window
}

func TestDatabaseEndpointDefaultsToOffWithNoPort(t *testing.T) {
	store := testStore(t)
	got, err := store.GetDatabaseEndpoint(context.Background(), "proj-never-asked")
	if err != nil {
		t.Fatalf("GetDatabaseEndpoint: %v", err)
	}
	want := domain.DefaultDBEndpoint("proj-never-asked")
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestAllocateTakesAPortInsideTheWindowAndLeavesTheEndpointOff(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31000, 31099)

	ep, err := store.AllocateDatabaseEndpointPort(context.Background(), "proj-alloc", domain.DBEndpointRolePostgres, window, testQuarantine)
	if err != nil {
		t.Fatalf("AllocateDatabaseEndpointPort: %v", err)
	}
	if !window.Contains(ep.Port) {
		t.Fatalf("port %d is outside %+v", ep.Port, window)
	}
	if ep.PublicEnabled {
		t.Fatal("allocation must not publish the endpoint; only an observed Service does")
	}
	if !ep.RequireTLS {
		t.Fatal("a new endpoint must require TLS")
	}
}

func TestAllocateIsIdempotentSoAResumeKeepsItsPort(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31100, 31199)
	ctx := context.Background()

	first, err := store.AllocateDatabaseEndpointPort(ctx, "proj-keep", domain.DBEndpointRolePostgres, window, testQuarantine)
	if err != nil {
		t.Fatalf("first allocate: %v", err)
	}
	second, err := store.AllocateDatabaseEndpointPort(ctx, "proj-keep", domain.DBEndpointRolePostgres, window, testQuarantine)
	if err != nil {
		t.Fatalf("second allocate: %v", err)
	}
	if first.Port != second.Port {
		t.Fatalf("port moved from %d to %d", first.Port, second.Port)
	}
}

func TestAllocateNeverHandsTheSamePortToTwoProjects(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31200, 31207) // exactly 8 ports
	ctx := context.Background()

	const projects = 8
	ports := make(chan int, projects)
	var wg sync.WaitGroup
	for i := 0; i < projects; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ep, err := store.AllocateDatabaseEndpointPort(ctx, projectRefFor(n), domain.DBEndpointRolePostgres, window, testQuarantine)
			if err != nil {
				t.Errorf("allocate %d: %v", n, err)
				return
			}
			ports <- ep.Port
		}(i)
	}
	wg.Wait()
	close(ports)

	seen := map[int]bool{}
	for port := range ports {
		if seen[port] {
			t.Fatalf("port %d was handed out twice", port)
		}
		seen[port] = true
	}
	if len(seen) != projects {
		t.Fatalf("allocated %d distinct ports, want %d", len(seen), projects)
	}
}

func TestAllocateRefusesWhenTheWindowIsFull(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31300, 31301)
	ctx := context.Background()

	for _, id := range []string{"proj-full-a", "proj-full-b"} {
		if _, err := store.AllocateDatabaseEndpointPort(ctx, id, domain.DBEndpointRolePostgres, window, testQuarantine); err != nil {
			t.Fatalf("allocate %s: %v", id, err)
		}
	}
	_, err := store.AllocateDatabaseEndpointPort(ctx, "proj-full-c", domain.DBEndpointRolePostgres, window, testQuarantine)
	if !errors.Is(err, storage.ErrDBEndpointPortsExhausted) {
		t.Fatalf("error = %v, want ErrDBEndpointPortsExhausted", err)
	}
}

func TestReleasedPortStaysInQuarantineForTheWholeWindow(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31400, 31400) // the one port there is
	ctx := context.Background()

	first, err := store.AllocateDatabaseEndpointPort(ctx, "proj-quar-a", domain.DBEndpointRolePostgres, window, testQuarantine)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-quar-a", domain.DBEndpointRolePostgres, time.Now()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-quar-b", domain.DBEndpointRolePostgres, window, testQuarantine); !errors.Is(err, storage.ErrDBEndpointPortsExhausted) {
		t.Fatalf("a freed port was reusable immediately: err = %v", err)
	}
	// Released long enough ago that the window has passed.
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-quar-a", domain.DBEndpointRolePostgres, time.Now()); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	reissued, err := store.AllocateDatabaseEndpointPort(ctx, "proj-quar-b", domain.DBEndpointRolePostgres, window, time.Nanosecond)
	if err != nil {
		t.Fatalf("allocate after the window passed: %v", err)
	}
	if reissued.Port != first.Port {
		t.Fatalf("reissued %d, want the freed %d", reissued.Port, first.Port)
	}
}

func TestReleaseIsIdempotentForAProjectHoldingNoPort(t *testing.T) {
	store := testStore(t)
	if err := store.ReleaseDatabaseEndpointPort(context.Background(), "proj-nothing", domain.DBEndpointRolePostgres, time.Now()); err != nil {
		t.Fatalf("ReleaseDatabaseEndpointPort: %v", err)
	}
}

func TestSetPublicAndRequireTLSSurviveEachOther(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31500, 31599)
	ctx := context.Background()

	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-flags", domain.DBEndpointRolePostgres, window, testQuarantine); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	ep, err := store.SetDatabaseEndpointRequireTLS(ctx, "proj-flags", false)
	if err != nil {
		t.Fatalf("SetDatabaseEndpointRequireTLS: %v", err)
	}
	if ep.RequireTLS {
		t.Fatal("require TLS was not turned off")
	}
	ep, err = store.SetDatabaseEndpointPublic(ctx, "proj-flags", true)
	if err != nil {
		t.Fatalf("SetDatabaseEndpointPublic: %v", err)
	}
	if !ep.PublicEnabled || ep.RequireTLS {
		t.Fatalf("flags clobbered each other: %+v", ep)
	}
	if !window.Contains(ep.Port) {
		t.Fatalf("port lost: %+v", ep)
	}
}

func TestTurningTheEndpointOffKeepsTheTLSChoice(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if _, err := store.SetDatabaseEndpointRequireTLS(ctx, "proj-tls-memory", false); err != nil {
		t.Fatalf("SetDatabaseEndpointRequireTLS: %v", err)
	}
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-tls-memory", domain.DBEndpointRolePostgres, time.Now()); err != nil {
		t.Fatalf("release: %v", err)
	}
	ep, err := store.GetDatabaseEndpoint(ctx, "proj-tls-memory")
	if err != nil {
		t.Fatalf("GetDatabaseEndpoint: %v", err)
	}
	if ep.RequireTLS {
		t.Fatal("the TLS choice is a setting of the project and must survive a release")
	}
	if ep.Port != 0 || ep.PublicEnabled {
		t.Fatalf("release left the endpoint live: %+v", ep)
	}
}

func TestDeleteRemovesTheRowButNotTheQuarantine(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31600, 31600)
	ctx := context.Background()

	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-gone", domain.DBEndpointRolePostgres, window, testQuarantine); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := store.ReleaseDatabaseEndpointPort(ctx, "proj-gone", domain.DBEndpointRolePostgres, time.Now()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.DeleteDatabaseEndpoint(ctx, "proj-gone"); err != nil {
		t.Fatalf("DeleteDatabaseEndpoint: %v", err)
	}
	if err := store.DeleteDatabaseEndpoint(ctx, "proj-gone"); err != nil {
		t.Fatalf("delete is not idempotent: %v", err)
	}
	if _, err := store.AllocateDatabaseEndpointPort(ctx, "proj-after", domain.DBEndpointRolePostgres, window, testQuarantine); !errors.Is(err, storage.ErrDBEndpointPortsExhausted) {
		t.Fatalf("deleting the project let its port out of quarantine: err = %v", err)
	}
}

func TestStoreSatisfiesTheDatabaseEndpointStore(t *testing.T) {
	var _ storage.DatabaseEndpointStore = (*Store)(nil)
}

// projectRefFor builds distinct project ids for the concurrency test.
func projectRefFor(n int) string {
	return "proj-conc-" + string(rune('a'+n))
}

func TestAllocateRefusesAWindowWithNoPortsInIt(t *testing.T) {
	store := testStore(t)
	_, err := store.AllocateDatabaseEndpointPort(context.Background(), "proj-nowindow", domain.DBEndpointRolePostgres, domain.PortRange{}, testQuarantine)
	if !errors.Is(err, domain.ErrInvalidPortRange) {
		t.Fatalf("error = %v, want ErrInvalidPortRange", err)
	}
}

// TestDatabaseEndpointOperationsFailLoudlyWhenTheDatabaseIsGone pins that
// every call reports a platform-database failure rather than answering from
// nothing. A read that silently returned "no endpoint" would look exactly
// like a project that never opted in, and a release that silently succeeded
// would free a port that is still answering.
func TestDatabaseEndpointOperationsFailLoudlyWhenTheDatabaseIsGone(t *testing.T) {
	store := testStore(t)
	window := testWindow(t, 31700, 31799)
	ctx := context.Background()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	calls := map[string]func() error{
		"get": func() error {
			_, err := store.GetDatabaseEndpoint(ctx, "proj-gone-db")
			return err
		},
		"allocate": func() error {
			_, err := store.AllocateDatabaseEndpointPort(ctx, "proj-gone-db", domain.DBEndpointRolePostgres, window, testQuarantine)
			return err
		},
		"set public": func() error {
			_, err := store.SetDatabaseEndpointPublic(ctx, "proj-gone-db", true)
			return err
		},
		"set require tls": func() error {
			_, err := store.SetDatabaseEndpointRequireTLS(ctx, "proj-gone-db", false)
			return err
		},
		"release": func() error {
			return store.ReleaseDatabaseEndpointPort(ctx, "proj-gone-db", domain.DBEndpointRolePostgres, time.Now())
		},
		"delete": func() error { return store.DeleteDatabaseEndpoint(ctx, "proj-gone-db") },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("%s succeeded against a closed platform database", name)
		}
	}
}

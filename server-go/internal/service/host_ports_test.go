package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-592: a single host's project holds its host port in the same allocator
// public endpoints use, so it is unique, kept, and quarantined when freed.
func TestHostPortsAreHeldUntilTheProjectGoes(t *testing.T) {
	store := newFakeEndpointStore()
	window, _ := domain.NewPortRange(30000, 30999)
	ports := NewHostPortAllocator(store, window, 30*24*time.Hour)
	ctx := context.Background()
	first, err := ports.Allocate(ctx, "p1")
	if err != nil || first != 30000 {
		t.Fatalf("first %d %v", first, err)
	}
	again, _ := ports.Allocate(ctx, "p1")
	other, _ := ports.Allocate(ctx, "p2")
	if again != first || other == first {
		t.Fatalf("again %d other %d", again, other)
	}
	if err := ports.Release(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if !store.quarantined(first) || len(store.deletes) != 1 {
		t.Fatalf("released port not quarantined: %v", store.deletes)
	}
}

// EXC-594: a DocumentDB project's gateway holds a second port, released with
// the first.
func TestADocumentDBProjectHoldsAMongoPortToo(t *testing.T) {
	store := newFakeEndpointStore()
	window, _ := domain.NewPortRange(30000, 30999)
	ports := NewHostPortAllocator(store, window, 30*24*time.Hour)
	ctx := context.Background()
	postgres, _ := ports.Allocate(ctx, "p1")
	mongo, err := ports.AllocateMongo(ctx, "p1")
	again, _ := ports.AllocateMongo(ctx, "p1")
	if err != nil || mongo == postgres || again != mongo {
		t.Fatalf("postgres %d mongo %d again %d %v", postgres, mongo, again, err)
	}
	if err := ports.Release(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if !store.quarantined(postgres) || !store.quarantined(mongo) {
		t.Fatal("both ports must be quarantined")
	}
}

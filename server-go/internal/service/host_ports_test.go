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

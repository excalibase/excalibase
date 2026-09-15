//go:build integration

package edgefn_test

import (
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// Exercises PostgresEgressStore against a real Postgres and proves migration
// 000014 applies (pgstore.New runs migrations on connect).
func newPGEgressStore(t *testing.T) *edgefn.PostgresEgressStore {
	t.Helper()
	return edgefn.NewPostgresEgressStore(newPGPlatformDB(t))
}

func TestPGEgressStore_UnsetProjectIsEmptyNotNil(t *testing.T) {
	s := newPGEgressStore(t)
	got, err := s.GetEgressHosts("proj_never_set")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("unset project must read as an empty (non-nil) list, got %#v", got)
	}
}

func TestPGEgressStore_SetGetRoundTrip(t *testing.T) {
	s := newPGEgressStore(t)
	hosts := []string{"*.amazonaws.com", "api.stripe.com:443"}
	if err := s.SetEgressHosts("proj_egress1", hosts); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.GetEgressHosts("proj_egress1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, hosts) {
		t.Fatalf("round-trip: got %v want %v", got, hosts)
	}
}

func TestPGEgressStore_SetReplacesAndClears(t *testing.T) {
	s := newPGEgressStore(t)
	if err := s.SetEgressHosts("proj_egress2", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEgressHosts("proj_egress2", []string{"b.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEgressHosts("proj_egress2")
	if !reflect.DeepEqual(got, []string{"b.example.com"}) {
		t.Fatalf("second Set must replace, got %v", got)
	}
	if err := s.SetEgressHosts("proj_egress2", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetEgressHosts("proj_egress2")
	if got == nil || len(got) != 0 {
		t.Fatalf("clearing must read back as empty non-nil, got %#v", got)
	}
}

func TestPGEgressStore_ProjectsAreIsolated(t *testing.T) {
	s := newPGEgressStore(t)
	if err := s.SetEgressHosts("proj_egress_a", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEgressHosts("proj_egress_b")
	if len(got) != 0 {
		t.Fatalf("project b must not see project a's allowlist: %v", got)
	}
}

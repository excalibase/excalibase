//go:build integration

package postgres

import (
	"context"
	"reflect"
	"testing"
)

// Exercises the ProjectCorsStore against a real Postgres and proves
// migration 000018 applies (New runs migrations on connect).

func TestProjectCors_UnsetProjectIsEmptyNotNil(t *testing.T) {
	store := testStore(t)
	got, err := store.GetCorsOrigins(context.Background(), "proj_never_set")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("unset project must read as an empty (non-nil) list, got %#v", got)
	}
}

func TestProjectCors_SetGetRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	origins := []string{"http://localhost:5173", "https://app.example.com"}
	if err := store.SetCorsOrigins(ctx, "proj_cors1", origins); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.GetCorsOrigins(ctx, "proj_cors1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, origins) {
		t.Fatalf("round-trip: got %v want %v", got, origins)
	}
}

func TestProjectCors_SetReplacesAndClears(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.SetCorsOrigins(ctx, "proj_cors2", []string{"https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCorsOrigins(ctx, "proj_cors2", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCorsOrigins(ctx, "proj_cors2")
	if !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("second Set must replace, got %v", got)
	}
	if err := store.SetCorsOrigins(ctx, "proj_cors2", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetCorsOrigins(ctx, "proj_cors2")
	if got == nil || len(got) != 0 {
		t.Fatalf("clearing must read back as empty non-nil, got %#v", got)
	}
}

func TestProjectCors_ProjectsAreIsolated(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.SetCorsOrigins(ctx, "proj_cors_a", []string{"https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCorsOrigins(ctx, "proj_cors_b")
	if len(got) != 0 {
		t.Fatalf("project b must not see project a's allowlist: %v", got)
	}
}

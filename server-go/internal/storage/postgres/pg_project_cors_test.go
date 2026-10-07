//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
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

func TestProjectCors_ConcurrentAddsAreAllKept(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.SetCorsOrigins(ctx, "proj_add", []string{"http://localhost:5173"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, origin := range []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"} {
		wg.Add(1)
		go func(origin string) {
			defer wg.Done()
			if _, _, err := store.AddCorsOrigin(ctx, "proj_add", origin, ""); err != nil {
				t.Errorf("add %s: %v", origin, err)
			}
		}(origin)
	}
	wg.Wait()
	got, _ := store.GetCorsOrigins(ctx, "proj_add")
	want := []string{"http://localhost:5173", "https://a.example.com", "https://b.example.com", "https://c.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent adds lost an origin: got %v want %v", got, want)
	}

	// No row yet, an origin already present, the wildcard and the cap.
	added, origins, err := store.AddCorsOrigin(ctx, "proj_new", "https://a.example.com", "")
	if err != nil || !added || !reflect.DeepEqual(origins, []string{"https://a.example.com"}) {
		t.Fatalf("first add on a project without a row: %v %v %v", added, origins, err)
	}
	if added, _, err := store.AddCorsOrigin(ctx, "proj_new", "https://a.example.com", "app-1"); err != nil || added {
		t.Fatalf("an origin already present is not added again: %v %v", added, err)
	}
	if released, err := store.ReleaseAppCorsOrigins(ctx, "proj_new", "app-1"); err != nil || len(released) != 0 {
		t.Fatalf("an app that added nothing owns nothing: %v %v", released, err)
	}
	if err := store.SetCorsOrigins(ctx, "proj_wild", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	if added, origins, err := store.AddCorsOrigin(ctx, "proj_wild", "https://a.example.com", ""); err != nil || added || !reflect.DeepEqual(origins, []string{"*"}) {
		t.Fatalf("the wildcard already allows every origin: %v %v %v", added, origins, err)
	}
	full := make([]string, 0, domain.MaxCorsOrigins)
	for i := range domain.MaxCorsOrigins {
		full = append(full, fmt.Sprintf("https://h%02d.example.com", i))
	}
	if err := store.SetCorsOrigins(ctx, "proj_full", full); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddCorsOrigin(ctx, "proj_full", "https://one-more.example.com", ""); !errors.Is(err, domain.ErrInvalidCorsOrigin) {
		t.Fatalf("past the cap: err = %v", err)
	}
}

func TestProjectCors_AnAppReleasesOnlyTheOriginsItAdded(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const web, renamed = "https://web.apps.example.com", "https://shop.apps.example.com"
	if err := store.SetCorsOrigins(ctx, "proj_rel", []string{"https://mine.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{web, renamed} {
		if _, _, err := store.AddCorsOrigin(ctx, "proj_rel", origin, "app-web"); err != nil {
			t.Fatal(err)
		}
	}
	released, err := store.ReleaseAppCorsOrigins(ctx, "proj_rel", "app-web")
	if err != nil || !reflect.DeepEqual(released, []string{web, renamed}) {
		t.Fatalf("release: %v %v", released, err)
	}
	if got, _ := store.GetCorsOrigins(ctx, "proj_rel"); !reflect.DeepEqual(got, []string{"https://mine.example.com"}) {
		t.Fatalf("after release: %v", got)
	}
	if released, err := store.ReleaseAppCorsOrigins(ctx, "proj_rel", "app-web"); err != nil || len(released) != 0 {
		t.Fatalf("a second release finds nothing: %v %v", released, err)
	}

	// Removed by hand, then added back by hand: no longer the app's to remove.
	if _, _, err := store.AddCorsOrigin(ctx, "proj_rm", web, "app-web"); err != nil {
		t.Fatal(err)
	}
	removed, origins, err := store.RemoveCorsOrigin(ctx, "proj_rm", web)
	if err != nil || !removed || len(origins) != 0 {
		t.Fatalf("remove: %v %v %v", removed, origins, err)
	}
	if removed, _, err := store.RemoveCorsOrigin(ctx, "proj_rm", web); err != nil || removed {
		t.Fatalf("removing an absent origin changes nothing: %v %v", removed, err)
	}
	if _, _, err := store.AddCorsOrigin(ctx, "proj_rm", web, ""); err != nil {
		t.Fatal(err)
	}
	if released, err := store.ReleaseAppCorsOrigins(ctx, "proj_rm", "app-web"); err != nil || len(released) != 0 {
		t.Fatalf("release after a manual re-add: %v %v", released, err)
	}

	// A whole-list replace that drops the origin ends the app's claim too.
	if _, _, err := store.AddCorsOrigin(ctx, "proj_put", web, "app-web"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCorsOrigins(ctx, "proj_put", []string{"https://other.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCorsOrigins(ctx, "proj_put", []string{"https://other.example.com", web}); err != nil {
		t.Fatal(err)
	}
	if released, err := store.ReleaseAppCorsOrigins(ctx, "proj_put", "app-web"); err != nil || len(released) != 0 {
		t.Fatalf("an origin set by hand is not the app's to remove: %v %v", released, err)
	}
}

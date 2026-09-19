//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Exercises the ProjectAuthSettingsStore against a real Postgres and proves
// migration 000026 applies (New runs migrations on connect).

func saveAuthSettingsProject(t *testing.T, store *Store, projectID string) {
	t.Helper()
	if err := store.Save(&domain.DatabaseInstance{ProjectID: projectID, OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestProjectAuthSettings_UnsetProjectReadsAsZeroValueNotFound(t *testing.T) {
	store := testStore(t)
	got, ok, err := store.GetAuthSettings(context.Background(), "proj_never_set")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("unset project must report ok=false, got %+v", got)
	}
	if got != (domain.ProjectAuthSettings{}) {
		t.Fatalf("unset project must read as the zero value, got %+v", got)
	}
}

func TestProjectAuthSettings_SetGetRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_auth1")

	want := domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://app.example.com"}
	if err := store.SetAuthSettings(ctx, "proj_auth1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := store.GetAuthSettings(ctx, "proj_auth1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got != want {
		t.Fatalf("round-trip: ok=%v got %+v want %+v", ok, got, want)
	}
}

func TestProjectAuthSettings_SetReplaces(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_auth2")

	if err := store.SetAuthSettings(ctx, "proj_auth2", domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuthSettings(ctx, "proj_auth2", domain.ProjectAuthSettings{RequireEmailVerification: false, SiteURL: "https://b.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetAuthSettings(ctx, "proj_auth2")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	want := domain.ProjectAuthSettings{RequireEmailVerification: false, SiteURL: "https://b.example.com"}
	if got != want {
		t.Fatalf("second Set must replace, got %+v want %+v", got, want)
	}
}

func TestProjectAuthSettings_ProjectsAreIsolated(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_auth_a")
	saveAuthSettingsProject(t, store, "proj_auth_b")

	if err := store.SetAuthSettings(ctx, "proj_auth_a", domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetAuthSettings(ctx, "proj_auth_b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("project b must not see project a's settings: %+v", got)
	}
}

func TestProjectAuthSettings_SetUnknownProjectIsRejected(t *testing.T) {
	store := testStore(t)
	err := store.SetAuthSettings(context.Background(), "ghost", domain.ProjectAuthSettings{RequireEmailVerification: true})
	if err == nil {
		t.Error("writing settings for a project that does not exist must fail (FK guard against junk rows)")
	}
}

func TestProjectAuthSettings_CascadeDeletesWithInstance(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_auth_cascade")
	if err := store.SetAuthSettings(ctx, "proj_auth_cascade", domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://c.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("proj_auth_cascade"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.GetAuthSettings(ctx, "proj_auth_cascade"); ok {
		t.Error("auth settings row must cascade-delete with its project")
	}
}

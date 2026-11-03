//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	restoreSource = "proj-rsrc0001"
	restoreCopy   = "proj-rcpy0001"
	appOrigin     = "https://shop.apps.example.com"
)

// seedRestoreSource gives the source everything a user configures on the
// platform for a project's API: permissions, a tracked function and who may
// call it, CORS origins (one an app added) and auth settings (EXC-639).
func seedRestoreSource(ctx context.Context, t *testing.T, store *Store) {
	t.Helper()
	perms := NewPermissions(store)
	session := "hasura_session"
	steps := []func() error{
		func() error {
			_, err := perms.PutPermission(ctx, domain.TablePermission{ProjectID: restoreSource, Table: "public.workspaces",
				Role: "authenticated", Operation: domain.PermissionSelect, Definition: json.RawMessage(`{"columns":["id","name"],"filter":{}}`)})
			return err
		},
		func() error {
			return perms.TrackFunction(ctx, domain.TrackedFunction{ProjectID: restoreSource, Function: "public.search",
				ExposedAs: "QUERY", InferPermissions: false, SessionArgument: &session})
		},
		func() error { return perms.PutFunctionPermission(ctx, restoreSource, "public.search", "authenticated") },
		func() error { return store.SetCorsOrigins(ctx, restoreSource, []string{"https://app.example.com"}) },
		func() error {
			_, _, err := store.AddCorsOrigin(ctx, restoreSource, appOrigin, "app-shop")
			return err
		},
		func() error {
			return store.SetAuthSettings(ctx, restoreSource, domain.ProjectAuthSettings{
				RequireEmailVerification: true, SiteURL: "https://app.example.com"})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}
}

func restoredSettingsStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t)
	for _, id := range []string{restoreSource, restoreCopy} {
		if err := store.Create(instanceRow(id, "org-restore")); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	return store
}

func TestCopyRestoredSettings_CarriesPermissionsCorsAndAuth(t *testing.T) {
	store := restoredSettingsStore(t)
	ctx := context.Background()
	seedRestoreSource(ctx, t, store)

	if err := store.CopyRestoredSettings(ctx, restoreSource, restoreCopy); err != nil {
		t.Fatalf("CopyRestoredSettings: %v", err)
	}

	perms := NewPermissions(store)
	source, copied := document(t, perms, restoreSource), document(t, perms, restoreCopy)
	if copied.Version == 0 {
		t.Error("the copy's permissions must be versioned like any written document")
	}
	if !reflect.DeepEqual(source.Tables, copied.Tables) {
		t.Errorf("table permissions: copy %+v, source %+v", copied.Tables, source.Tables)
	}
	if !reflect.DeepEqual(source.FunctionPermissions, copied.FunctionPermissions) {
		t.Errorf("function permissions: copy %+v, source %+v", copied.FunctionPermissions, source.FunctionPermissions)
	}
	if len(copied.Functions) != 1 || copied.Functions[0].Function != "public.search" ||
		copied.Functions[0].InferPermissions || copied.Functions[0].SessionArgument == nil {
		t.Errorf("tracked functions: copy %+v", copied.Functions)
	}

	origins, err := store.GetCorsOrigins(ctx, restoreCopy)
	if err != nil {
		t.Fatal(err)
	}
	// The app is not copied, so neither is the origin it added.
	if !reflect.DeepEqual(origins, []string{"https://app.example.com"}) {
		t.Errorf("copy CORS origins = %v, want only the ones a person added", origins)
	}
	auth, ok, err := store.GetAuthSettings(ctx, restoreCopy)
	if err != nil || !ok || !auth.RequireEmailVerification || auth.SiteURL != "https://app.example.com" {
		t.Errorf("copy auth settings = %+v (stored %v, %v), want the source's", auth, ok, err)
	}
}

func TestCopyRestoredSettings_SourceWithNothingConfiguredCopiesNothing(t *testing.T) {
	store := restoredSettingsStore(t)
	ctx := context.Background()

	if err := store.CopyRestoredSettings(ctx, restoreSource, restoreCopy); err != nil {
		t.Fatalf("CopyRestoredSettings: %v", err)
	}
	doc := document(t, NewPermissions(store), restoreCopy)
	if len(doc.Tables)+len(doc.Functions)+len(doc.FunctionPermissions) != 0 {
		t.Errorf("copy permissions = %+v, want none", doc)
	}
	if _, ok, _ := store.GetAuthSettings(ctx, restoreCopy); ok {
		t.Error("a source without auth settings must leave the copy without a row")
	}
}

// The copy's settings belong to the copy: deleting it must take them along.
func TestCopyRestoredSettings_DeletingTheCopyRemovesThem(t *testing.T) {
	store := restoredSettingsStore(t)
	ctx := context.Background()
	seedRestoreSource(ctx, t, store)
	if err := store.CopyRestoredSettings(ctx, restoreSource, restoreCopy); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(restoreCopy); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if origins, _ := store.GetCorsOrigins(ctx, restoreCopy); len(origins) != 0 {
		t.Errorf("deleted copy kept CORS origins %v", origins)
	}
	if doc := document(t, NewPermissions(store), restoreCopy); len(doc.Tables) != 0 || doc.Version != 0 {
		t.Errorf("deleted copy kept permissions %+v", doc)
	}
	if doc := document(t, NewPermissions(store), restoreSource); len(doc.Tables) != 1 {
		t.Errorf("the source lost its permissions: %+v", doc)
	}
}

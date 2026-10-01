//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	permProject      = "proj-perm"
	permOtherProject = "proj-perm-other"
	ordersTable      = "public.orders"
	searchFunction   = "public.search_orders"
)

func permissionsStore(t *testing.T) (*Store, *PermissionStore) {
	t.Helper()
	store := testStore(t)
	for _, id := range []string{permProject, permOtherProject} {
		if err := store.Create(&domain.DatabaseInstance{ProjectID: id, OrgID: "org1", Status: "ACTIVE"}); err != nil {
			t.Fatalf("create project %s: %v", id, err)
		}
	}
	return store, NewPermissions(store)
}

func tablePermission(role, op, definition string) domain.TablePermission {
	return domain.TablePermission{ProjectID: permProject, Table: ordersTable, Role: role, Operation: op,
		Definition: json.RawMessage(definition)}
}

func document(t *testing.T, perms *PermissionStore, projectID string) *domain.PermissionDocument {
	t.Helper()
	doc, err := perms.Document(context.Background(), projectID)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	return doc
}

func TestPermissions_EmptyDocumentHasArraysAndVersionZero(t *testing.T) {
	_, perms := permissionsStore(t)
	doc := document(t, perms, permProject)
	raw, _ := json.Marshal(doc)
	want := `{"projectId":"proj-perm","version":0,"tables":[],"functions":[],"functionPermissions":[]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPermissions_PutGroupsByTableAndRoleAndBumpsVersion(t *testing.T) {
	_, perms := permissionsStore(t)
	ctx := context.Background()

	created, err := perms.PutPermission(ctx, tablePermission("user", "select", `{"filter":{},"columns":"*"}`))
	if err != nil || !created {
		t.Fatalf("first put: created=%v err=%v", created, err)
	}
	created, err = perms.PutPermission(ctx, tablePermission("user", "select", `{"filter":{"a":{"_eq":1}},"columns":["a"]}`))
	if err != nil || created {
		t.Fatalf("replacing put: created=%v err=%v", created, err)
	}
	if _, err := perms.PutPermission(ctx, tablePermission("user", "delete", `{"filter":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := perms.PutPermission(ctx, tablePermission("anon", "select", `{"filter":{},"columns":"*"}`)); err != nil {
		t.Fatal(err)
	}

	doc := document(t, perms, permProject)
	if doc.Version != 4 {
		t.Errorf("version = %d, want 4 (one per write)", doc.Version)
	}
	raw, _ := json.Marshal(doc.Tables)
	want := `[{"table":"public.orders","role":"anon","select":{"columns": "*", "filter": {}}},` +
		`{"table":"public.orders","role":"user","select":{"filter": {"a": {"_eq": 1}}, "columns": ["a"]},"delete":{"filter": {}}}]`
	if !jsonEqual(t, raw, []byte(want)) {
		t.Fatalf("tables = %s, want %s", raw, want)
	}
	if other := document(t, perms, permOtherProject); len(other.Tables) != 0 || other.Version != 0 {
		t.Fatalf("another project sees %+v", other)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

func TestPermissions_DeleteReportsAbsenceAndBumpsOnlyOnChange(t *testing.T) {
	_, perms := permissionsStore(t)
	ctx := context.Background()
	if err := perms.DeletePermission(ctx, permProject, ordersTable, "user", "select"); !errors.Is(err, storage.ErrPermissionNotFound) {
		t.Fatalf("absent delete = %v", err)
	}
	if _, err := perms.PutPermission(ctx, tablePermission("user", "select", `{"filter":{},"columns":"*"}`)); err != nil {
		t.Fatal(err)
	}
	if err := perms.DeletePermission(ctx, permOtherProject, ordersTable, "user", "select"); !errors.Is(err, storage.ErrPermissionNotFound) {
		t.Fatalf("delete from another project = %v", err)
	}
	if err := perms.DeletePermission(ctx, permProject, ordersTable, "user", "select"); err != nil {
		t.Fatal(err)
	}
	doc := document(t, perms, permProject)
	if len(doc.Tables) != 0 || doc.Version != 2 {
		t.Fatalf("after delete: %+v", doc)
	}
}

func TestPermissions_DatabaseRefusesWhatValidationRefuses(t *testing.T) {
	_, perms := permissionsStore(t)
	ctx := context.Background()
	for name, perm := range map[string]domain.TablePermission{
		"service role":      tablePermission("service", "select", `{}`),
		"upper-case role":   tablePermission("User", "select", `{}`),
		"unknown operation": tablePermission("user", "upsert", `{}`),
		"array definition":  tablePermission("user", "select", `[]`),
		"unknown project":   {ProjectID: "nope", Table: ordersTable, Role: "user", Operation: "select", Definition: json.RawMessage(`{}`)},
	} {
		if _, err := perms.PutPermission(ctx, perm); err == nil {
			t.Errorf("%s must be refused by the database", name)
		}
	}
}

func TestPermissions_TrackedFunctionsAndFunctionPermissions(t *testing.T) {
	_, perms := permissionsStore(t)
	ctx := context.Background()
	if err := perms.PutFunctionPermission(ctx, permProject, searchFunction, "editor"); !errors.Is(err, storage.ErrFunctionNotTracked) {
		t.Fatalf("permission for an untracked function = %v", err)
	}
	session := "session"
	fn := domain.TrackedFunction{ProjectID: permProject, Function: searchFunction, ExposedAs: domain.ExposeAsQuery,
		InferPermissions: true, SessionArgument: &session}
	if err := perms.TrackFunction(ctx, fn); err != nil {
		t.Fatal(err)
	}
	if err := perms.TrackFunction(ctx, fn); !errors.Is(err, storage.ErrFunctionAlreadyTracked) {
		t.Fatalf("tracking twice = %v", err)
	}
	for _, role := range []string{"editor", "editor", "user"} {
		if err := perms.PutFunctionPermission(ctx, permProject, searchFunction, role); err != nil {
			t.Fatalf("grant %s: %v", role, err)
		}
	}
	doc := document(t, perms, permProject)
	raw, _ := json.Marshal(doc)
	want := `{"projectId":"proj-perm","version":4,"tables":[],` +
		`"functions":[{"function":"public.search_orders","exposedAs":"QUERY","inferPermissions":true,"sessionArgument":"session"}],` +
		`"functionPermissions":[{"function":"public.search_orders","role":"editor"},{"function":"public.search_orders","role":"user"}]}`
	if string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}

	if err := perms.DeleteFunctionPermission(ctx, permProject, searchFunction, "user"); err != nil {
		t.Fatal(err)
	}
	if err := perms.DeleteFunctionPermission(ctx, permProject, searchFunction, "user"); !errors.Is(err, storage.ErrFunctionPermissionNotFound) {
		t.Fatalf("deleting twice = %v", err)
	}
	if err := perms.UntrackFunction(ctx, permProject, searchFunction); err != nil {
		t.Fatal(err)
	}
	if err := perms.UntrackFunction(ctx, permProject, searchFunction); !errors.Is(err, storage.ErrFunctionNotTracked) {
		t.Fatalf("untracking twice = %v", err)
	}
	doc = document(t, perms, permProject)
	if len(doc.Functions) != 0 || len(doc.FunctionPermissions) != 0 {
		t.Fatalf("untracking must take the function permissions with it: %+v", doc)
	}
}

func TestPermissions_ProjectDeletionCascades(t *testing.T) {
	store, perms := permissionsStore(t)
	ctx := context.Background()
	if _, err := perms.PutPermission(ctx, tablePermission("user", "select", `{"filter":{},"columns":"*"}`)); err != nil {
		t.Fatal(err)
	}
	if err := perms.TrackFunction(ctx, domain.TrackedFunction{ProjectID: permProject, Function: searchFunction, ExposedAs: domain.ExposeAsQuery}); err != nil {
		t.Fatal(err)
	}
	if err := perms.PutFunctionPermission(ctx, permProject, searchFunction, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`DELETE FROM database_instances WHERE project_id = $1`, permProject); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for _, table := range []string{"api_permissions", "tracked_functions", "function_permissions", "permission_versions"} {
		var n int
		if err := store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id = $1`, permProject).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s kept %d rows of a deleted project", table, n)
		}
	}
}

const (
	migrationBeforePermissions = 64
	migrationPermissions       = 65
	migrationLegacyDropped     = 66
)

var legacyAccessTables = []string{"rls_policies", "column_policies", "table_grants", "legacy_permissions_migrated"}

func tableExists(t *testing.T, store *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := store.DB().QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestPermissions_MigrationDownAndUp(t *testing.T) {
	store := testStore(t)
	tables := []string{"api_permissions", "tracked_functions", "function_permissions", "permission_versions"}
	if err := store.m.Migrate(migrationBeforePermissions); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforePermissions, err)
	}
	for _, table := range append(tables, "legacy_permissions_migrated") {
		if tableExists(t, store, table) {
			t.Errorf("%s survived the down migration", table)
		}
	}
	if err := store.m.Migrate(migrationPermissions); err != nil {
		t.Fatalf("migrate up to %d: %v", migrationPermissions, err)
	}
	for _, table := range tables {
		if !tableExists(t, store, table) {
			t.Errorf("%s missing after the up migration", table)
		}
	}
}

func TestPermissions_LegacyAccessStoresAreGone(t *testing.T) {
	store := testStore(t)
	for _, table := range legacyAccessTables {
		if tableExists(t, store, table) {
			t.Errorf("%s still exists; permissions are the only access store", table)
		}
	}
	if !tableExists(t, store, "api_permissions") {
		t.Fatal("dropping the legacy stores must leave permissions alone")
	}
}

func TestPermissions_LegacyDropDownRecreatesEmptyStores(t *testing.T) {
	store := testStore(t)
	if err := store.m.Migrate(migrationPermissions); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationPermissions, err)
	}
	for _, table := range legacyAccessTables {
		if !tableExists(t, store, table) {
			t.Errorf("%s missing after the down migration", table)
		}
	}
	if _, err := store.DB().Exec(`INSERT INTO table_grants (id, project_id, resource, operations, role_name)
		VALUES ('g1', 'p', 'public.orders', '{SELECT}', 'service')`); err == nil {
		t.Error("the recreated table_grants lost its role constraint")
	}
	if err := store.m.Migrate(migrationLegacyDropped); err != nil {
		t.Fatalf("migrate up to %d: %v", migrationLegacyDropped, err)
	}
	for _, table := range legacyAccessTables {
		if tableExists(t, store, table) {
			t.Errorf("%s survived the up migration", table)
		}
	}
}

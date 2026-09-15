package edgefn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// Exercises PostgresFunctionStore against a real Postgres (EXC-333). Also proves
// migration 000013 applies, since pgstore.New runs migrations on connect.
func newPGFunctionStore(t *testing.T) *edgefn.PostgresFunctionStore {
	t.Helper()
	ctx := context.Background()

	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("platform_test"),
		postgres.WithUsername("platform"),
		postgres.WithPassword("edgefn-itest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://platform:edgefn-itest@%s:%s/platform_test?sslmode=disable",
		host, port.Port())

	store, err := pgstore.New(dsn)
	if err != nil {
		t.Fatalf("pgstore.New (runs migrations): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return edgefn.NewPostgresFunctionStore(store.DB())
}

func sampleFn(projectID, id string) *edgefn.Function {
	return &edgefn.Function{
		ID: id, ProjectID: projectID, Name: id, Active: true,
		Files: []edgefn.File{{Path: "index.ts", Content: `export default () => new Response("v1");`}},
	}
}

func TestPGStore_SaveGetRoundTrip(t *testing.T) {
	s := newPGFunctionStore(t)
	fn := sampleFn("proj_itest1", "hello")

	if err := s.Save(fn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fn.Version != 1 {
		t.Errorf("first save must be version 1, got %d", fn.Version)
	}
	if fn.CreatedAt.IsZero() {
		t.Error("created_at should be set by the store")
	}

	got, err := s.Get("proj_itest1", "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected the saved function back")
	}
	if got.ID != "hello" || got.ProjectID != "proj_itest1" || !got.Active {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "index.ts" {
		t.Errorf("files did not round-trip: %+v", got.Files)
	}
}

// The doc column must preserve caller-set optional fields — the reason it's
// JSONB rather than a column per field. Note RuntimeShape/Kind/HttpRoutes/
// CronJobs are NOT asserted here: Validate() re-bundles on every Save and
// *derives* those from the emitted JS, so a caller-supplied value is expected
// to be overwritten.
func TestPGStore_PreservesOptionalFields(t *testing.T) {
	s := newPGFunctionStore(t)
	fn := sampleFn("proj_itest1", "rich")
	fn.Description = "a described function"
	fn.IsInternal = true
	fn.ExportMetadata = json.RawMessage(`[{"name":"listUsers","kind":"query"}]`)
	fn.SchemaJSON = json.RawMessage(`{"tables":{"users":{}}}`)

	if err := s.Save(fn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get("proj_itest1", "rich")
	if err != nil || got == nil {
		t.Fatalf("Get: %v (got=%v)", err, got)
	}
	if got.Description != "a described function" || !got.IsInternal {
		t.Errorf("scalar fields lost: desc=%q internal=%v", got.Description, got.IsInternal)
	}
	if len(got.ExportMetadata) == 0 {
		t.Error("exportMetadata was dropped by the store")
	}
	if len(got.SchemaJSON) == 0 {
		t.Error("schemaJson was dropped by the store")
	}
	// Derived-on-bundle: a plain default-export handler is the v1 shape.
	if got.RuntimeShape != "v1" {
		t.Errorf("runtimeShape should be derived as v1, got %q", got.RuntimeShape)
	}
}

// EXC-334 regression: Save() validates by bundling, so a function importing a
// shared module must still save. Before shared files were threaded through
// validation this failed with "module not found".
func TestPGStore_SaveResolvesSharedImport(t *testing.T) {
	s := newPGFunctionStore(t)
	if err := s.PutSharedFile("proj_itest1", edgefn.File{
		Path: "_shared/cors.ts", Content: `export const CORS = "yes";`}); err != nil {
		t.Fatalf("PutSharedFile: %v", err)
	}

	fn := &edgefn.Function{
		ID: "uses-shared", ProjectID: "proj_itest1", Name: "uses-shared", Active: true,
		Files: []edgefn.File{{Path: "index.ts", Content: `
import { CORS } from "../_shared/cors.ts";
export default () => new Response(CORS);`}},
	}
	if err := s.Save(fn); err != nil {
		t.Fatalf("saving a function that imports _shared/ must succeed: %v", err)
	}

	// And the same function must fail when the shared module is gone.
	if err := s.DeleteSharedFile("proj_itest1", "_shared/cors.ts"); err != nil {
		t.Fatalf("DeleteSharedFile: %v", err)
	}
	if err := s.Save(fn); err == nil {
		t.Error("expected save to fail once the shared module was removed")
	}
}

// Re-saving must bump version and keep the original created_at — the filesystem
// store's contract, which callers rely on for deploy versioning.
func TestPGStore_SaveBumpsVersionKeepsCreatedAt(t *testing.T) {
	s := newPGFunctionStore(t)
	fn := sampleFn("proj_itest1", "versioned")
	if err := s.Save(fn); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	firstCreated := fn.CreatedAt

	again := sampleFn("proj_itest1", "versioned")
	again.Files[0].Content = `export default () => new Response("v2");`
	if err := s.Save(again); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if again.Version != 2 {
		t.Errorf("second save should be version 2, got %d", again.Version)
	}
	if !again.CreatedAt.Equal(firstCreated) {
		t.Errorf("created_at must be preserved: first=%v second=%v", firstCreated, again.CreatedAt)
	}

	got, _ := s.Get("proj_itest1", "versioned")
	if got.Version != 2 {
		t.Errorf("persisted doc should carry version 2, got %d", got.Version)
	}
	if got.Files[0].Content != again.Files[0].Content {
		t.Error("second save did not replace the files")
	}
}

// Missing function returns (nil, nil) — filesystem-store parity the handlers
// depend on to distinguish "absent" from "error".
func TestPGStore_GetMissingReturnsNilNil(t *testing.T) {
	s := newPGFunctionStore(t)
	got, err := s.Get("proj_itest1", "nope")
	if err != nil {
		t.Fatalf("expected no error for a missing function, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for a missing function, got %+v", got)
	}
}

func TestPGStore_ListIsProjectScoped(t *testing.T) {
	s := newPGFunctionStore(t)
	if err := s.Save(sampleFn("proj_itest1", "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sampleFn("proj_itest1", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sampleFn("proj_itest2", "c")); err != nil {
		t.Fatal(err)
	}

	one, err := s.List("proj_itest1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(one) != 2 {
		t.Fatalf("expected 2 functions for proj_itest1, got %d", len(one))
	}
	for _, fn := range one {
		if fn.ProjectID != "proj_itest1" {
			t.Errorf("cross-project leak: %s belongs to %s", fn.ID, fn.ProjectID)
		}
	}
	empty, err := s.List("proj_itest3")
	if err != nil || len(empty) != 0 {
		t.Errorf("expected empty slice for unknown project, got %v (err=%v)", empty, err)
	}
}

func TestPGStore_Delete(t *testing.T) {
	s := newPGFunctionStore(t)
	if err := s.Save(sampleFn("proj_itest1", "gone")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("proj_itest1", "gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := s.Get("proj_itest1", "gone")
	if got != nil {
		t.Error("function should be gone after Delete")
	}
	// Deleting a missing function is a no-op, not an error.
	if err := s.Delete("proj_itest1", "gone"); err != nil {
		t.Errorf("second Delete should be a no-op, got %v", err)
	}
}

// EXC-334: shared modules persist per project and stay project-scoped.
func TestPGStore_SharedFilesRoundTrip(t *testing.T) {
	s := newPGFunctionStore(t)
	if err := s.PutSharedFile("proj_itest1", edgefn.File{
		Path: "_shared/cors.ts", Content: `export const C = 1;`}); err != nil {
		t.Fatalf("PutSharedFile: %v", err)
	}
	// Replacing the same path updates rather than duplicating.
	if err := s.PutSharedFile("proj_itest1", edgefn.File{
		Path: "_shared/cors.ts", Content: `export const C = 2;`}); err != nil {
		t.Fatalf("PutSharedFile (replace): %v", err)
	}

	files, err := s.SharedFiles("proj_itest1")
	if err != nil {
		t.Fatalf("SharedFiles: %v", err)
	}
	if len(files) != 1 || files[0].Content != `export const C = 2;` {
		t.Errorf("expected one updated shared file, got %+v", files)
	}
	if other, _ := s.SharedFiles("proj_itest2"); len(other) != 0 {
		t.Errorf("shared files must be project-scoped, got %+v", other)
	}

	if err := s.DeleteSharedFile("proj_itest1", "_shared/cors.ts"); err != nil {
		t.Fatalf("DeleteSharedFile: %v", err)
	}
	if files, _ = s.SharedFiles("proj_itest1"); len(files) != 0 {
		t.Errorf("expected no shared files after delete, got %+v", files)
	}
}

// A shared path outside _shared/ must be rejected before it can shadow a
// function's entry point.
func TestPGStore_RejectsBadSharedPath(t *testing.T) {
	s := newPGFunctionStore(t)
	if err := s.PutSharedFile("proj_itest1", edgefn.File{
		Path: "index.ts", Content: "x"}); err == nil {
		t.Error("expected a non-_shared path to be rejected")
	}
}

func TestPGStore_ProjectIDs_DistinctProjectsWithFunctions(t *testing.T) {
	s := newPGFunctionStore(t)
	for _, pair := range [][2]string{{"proj_pl1", "a"}, {"proj_pl1", "b"}, {"proj_pl2", "c"}} {
		if err := s.Save(sampleFn(pair[0], pair[1])); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.ProjectIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "proj_pl1" || ids[1] != "proj_pl2" {
		t.Fatalf("project ids: got %v", ids)
	}
	if err := s.Delete("proj_pl2", "c"); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ProjectIDs()
	if len(ids) != 1 || ids[0] != "proj_pl1" {
		t.Fatalf("after delete: got %v", ids)
	}
}

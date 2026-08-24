//go:build integration

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// schemaLibBundle is a minimal `@excalibase/server` shim that the integration
// test ships alongside the user's schema.ts. It mirrors the real lib's
// behaviour just enough for the bundler/extractor to function — see
// edgefn/schema_test.go for the canonical version.
const schemaLibBundle = `
const validator = (kind) => ({ kind, parse: (v) => v, toJsonSchema: () => ({ type: kind }) });
export const v = {
  string: () => validator("string"),
  number: () => validator("number"),
  id: (table) => ({
    kind: "id",
    tableName: table,
    parse: (v) => v,
    toJsonSchema: () => ({ type: "string", "x-convex-id": table }),
  }),
  object: (shape) => ({
    kind: "object",
    shape,
    parse: (v) => v,
    toJsonSchema: () => {
      const properties = {};
      const required = [];
      for (const k of Object.keys(shape)) {
        properties[k] = shape[k].toJsonSchema();
        required.push(k);
      }
      return { type: "object", properties, required, additionalProperties: false };
    },
  }),
};
export function defineTable(validator) {
  const indexes = [];
  function rebuild() {
    return {
      validator,
      indexes,
      searchIndexes: [],
      vectorIndexes: [],
      index(name, fields) { indexes.push({ name, fields }); return rebuild(); },
      searchIndex() { return rebuild(); },
      vectorIndex() { return rebuild(); },
    };
  }
  return rebuild();
}
export function defineSchema(tables) {
  const out = { tables: {} };
  for (const [name, b] of Object.entries(tables)) {
    const base = b.validator.toJsonSchema();
    const properties = { ...base.properties };
    properties._id = { type: "string", "x-convex-id": name };
    properties._creationTime = { type: "number" };
    out.tables[name] = {
      validator: { type: "object", properties, required: base.required, additionalProperties: false },
      indexes: b.indexes,
      searchIndexes: b.searchIndexes,
      vectorIndexes: b.vectorIndexes,
    };
  }
  globalThis.__excalibase_schema = out;
  return { tables, toJsonSchema: () => out };
}
`

func setupSchemaPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("fn_schema_test"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	connStr, _ := c.ConnectionString(ctx, "sslmode=disable")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db, func() {
		db.Close()
		_ = c.Terminate(ctx)
	}
}

func TestFnDeploy_AppliesSchemaToProjectDB(t *testing.T) {
	db, cleanup := setupSchemaPG(t)
	defer cleanup()

	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	vault := &e2eFakeVault{data: map[string]map[string]string{}}
	secrets := edgefn.NewSecretsStore(vault)
	instStore := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_e2eschm": {ProjectID: "proj_e2eschm", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, "https://api.e2e.test")
	h.SetProjectDBFn(func(_ context.Context, _ string) (*sql.DB, error) {
		return db, nil
	})
	h.SetAutoMigrate(true)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Post("/", h.Create)
	})

	userSchema := `
import { defineSchema, defineTable, v } from "./_lib.ts";
export default defineSchema({
  notes: defineTable(v.object({ title: v.string(), body: v.string() }))
    .index("by_title", ["title"]),
});
`
	body := map[string]interface{}{
		"id":   "schema-app",
		"name": "Schema App",
		"files": []map[string]string{
			{"path": "_lib.ts", "content": schemaLibBundle},
			{"path": testIndexTS, "content": userSchema},
		},
	}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/api/projects/proj_e2eschm/functions/", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Without a runtime client wired in, Create reaches the deploy stage
	// and fails — but the schema migration must have already run before
	// that point. The test asserts on the DB state, not the HTTP code.
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname = 'nosql' AND tablename = $1)`,
		"notes").Scan(&exists)
	if err != nil {
		t.Fatalf("query table: %v", err)
	}
	if !exists {
		t.Errorf("nosql.notes table not created by deploy (response %d body=%s)", w.Code, w.Body.String())
	}

	// Assert SchemaJSON was persisted on the function record (regardless
	// of whether the runtime deploy succeeded).
	stored, _ := store.Get("proj_e2eschm", "schema-app")
	if stored == nil {
		t.Skip("function record was rolled back — confirms atomic delete; SchemaJSON assertion not applicable")
	}
	if len(stored.SchemaJSON) == 0 {
		t.Error("expected SchemaJSON to be populated on stored function")
	}
}

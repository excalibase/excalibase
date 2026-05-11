//go:build integration

package edgefn

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupMigratorPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("migrate_test"),
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
	// Create the nosql schema that the migrator writes into.
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS nosql`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db, func() {
		db.Close()
		_ = c.Terminate(ctx)
	}
}

func makeSchema(t *testing.T, raw string) Schema {
	t.Helper()
	var s Schema
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return s
}

func TestApplySchema_RejectsBadIdentifiers(t *testing.T) {
	db, cleanup := setupMigratorPG(t)
	defer cleanup()
	cases := []struct {
		name string
		raw  string
	}{
		{"bad table", `{"tables": {"DROP TABLE": {"validator": {"type": "object", "properties": {}, "required": [], "additionalProperties": false}, "indexes": [], "searchIndexes": [], "vectorIndexes": []}}}`},
		{"bad index", `{"tables": {"users": {"validator": {"type": "object", "properties": {"name": {"type": "string"}}, "required": [], "additionalProperties": false}, "indexes": [{"name": "DROP idx", "fields": ["name"]}], "searchIndexes": [], "vectorIndexes": []}}}`},
		{"empty index fields", `{"tables": {"users": {"validator": {"type": "object", "properties": {"name": {"type": "string"}}, "required": [], "additionalProperties": false}, "indexes": [{"name": "by_x", "fields": []}], "searchIndexes": [], "vectorIndexes": []}}}`},
		{"bad field", `{"tables": {"users": {"validator": {"type": "object", "properties": {"name": {"type": "string"}}, "required": [], "additionalProperties": false}, "indexes": [{"name": "by_x", "fields": ["DROP TABLE"]}], "searchIndexes": [], "vectorIndexes": []}}}`},
		{"bad vector dims", `{"tables": {"users": {"validator": {"type": "object", "properties": {"emb": {"type": "array"}}, "required": [], "additionalProperties": false}, "indexes": [], "searchIndexes": [], "vectorIndexes": [{"name": "by_emb", "vectorField": "emb", "dimensions": -1, "filterFields": []}]}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := makeSchema(t, tc.raw)
			if err := ApplySchema(context.Background(), db, "proj_test_invalid", schema); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestApplySchema_CreatesTables(t *testing.T) {
	db, cleanup := setupMigratorPG(t)
	defer cleanup()
	raw := `{
		"tables": {
			"users": {
				"validator": {
					"type": "object",
					"properties": {
						"_id": { "type": "string", "x-convex-id": "users" },
						"_creationTime": { "type": "number" },
						"name": { "type": "string" }
					},
					"required": ["name"],
					"additionalProperties": false
				},
				"indexes": [{ "name": "by_name", "fields": ["name"] }],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	schema := makeSchema(t, raw)
	if err := ApplySchema(context.Background(), db, "proj_test0001", schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	var tableExists bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname = 'nosql' AND tablename = $1)`,
		"users").Scan(&tableExists)
	if err != nil {
		t.Fatalf("query table: %v", err)
	}
	if !tableExists {
		t.Error("nosql.users table not created")
	}
	var indexExists bool
	err = db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_indexes WHERE schemaname = 'nosql' AND indexname = $1)`,
		"users_by_name").Scan(&indexExists)
	if err != nil {
		t.Fatalf("query index: %v", err)
	}
	if !indexExists {
		t.Error("users_by_name index not created")
	}
}

func TestApplySchema_Idempotent(t *testing.T) {
	db, cleanup := setupMigratorPG(t)
	defer cleanup()
	raw := `{
		"tables": {
			"items": {
				"validator": {
					"type": "object",
					"properties": { "name": { "type": "string" } },
					"required": ["name"],
					"additionalProperties": false
				},
				"indexes": [{ "name": "by_name", "fields": ["name"] }],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	schema := makeSchema(t, raw)
	if err := ApplySchema(context.Background(), db, "proj_test0002", schema); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Re-apply — must not fail.
	if err := ApplySchema(context.Background(), db, "proj_test0002", schema); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

func TestApplySchema_AdditiveIndex(t *testing.T) {
	db, cleanup := setupMigratorPG(t)
	defer cleanup()
	raw1 := `{
		"tables": {
			"docs": {
				"validator": {
					"type": "object",
					"properties": {
						"title": { "type": "string" },
						"author": { "type": "string" }
					},
					"required": ["title"],
					"additionalProperties": false
				},
				"indexes": [{ "name": "by_title", "fields": ["title"] }],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	if err := ApplySchema(context.Background(), db, "proj_test0003", makeSchema(t, raw1)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	raw2 := `{
		"tables": {
			"docs": {
				"validator": {
					"type": "object",
					"properties": {
						"title": { "type": "string" },
						"author": { "type": "string" }
					},
					"required": ["title"],
					"additionalProperties": false
				},
				"indexes": [
					{ "name": "by_title", "fields": ["title"] },
					{ "name": "by_author", "fields": ["author"] }
				],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	if err := ApplySchema(context.Background(), db, "proj_test0003", makeSchema(t, raw2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_indexes WHERE schemaname = 'nosql' AND indexname = $1)`,
		"docs_by_author").Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Error("by_author index not added on second apply")
	}
}

func TestApplySchema_IndexRemovedIsNoOp(t *testing.T) {
	db, cleanup := setupMigratorPG(t)
	defer cleanup()
	rawWithIndex := `{
		"tables": {
			"things": {
				"validator": {
					"type": "object",
					"properties": { "tag": { "type": "string" } },
					"required": [],
					"additionalProperties": false
				},
				"indexes": [{ "name": "by_tag", "fields": ["tag"] }],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	rawWithoutIndex := `{
		"tables": {
			"things": {
				"validator": {
					"type": "object",
					"properties": { "tag": { "type": "string" } },
					"required": [],
					"additionalProperties": false
				},
				"indexes": [],
				"searchIndexes": [],
				"vectorIndexes": []
			}
		}
	}`
	if err := ApplySchema(context.Background(), db, "proj_test0004", makeSchema(t, rawWithIndex)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := ApplySchema(context.Background(), db, "proj_test0004", makeSchema(t, rawWithoutIndex)); err != nil {
		t.Fatalf("second apply (with index removed): %v", err)
	}
	// Index should still exist — additive-only migration policy.
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_indexes WHERE schemaname = 'nosql' AND indexname = $1)`,
		"things_by_tag").Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Error("by_tag index was dropped — migrator must be additive-only")
	}
}

func setupMigratorPGWithVector(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "pgvector/pgvector:pg16",
		postgres.WithDatabase("migrate_test"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres+pgvector: %v", err)
	}
	connStr, _ := c.ConnectionString(ctx, "sslmode=disable")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS nosql`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		t.Fatalf("create vector extension: %v", err)
	}
	return db, func() {
		db.Close()
		_ = c.Terminate(ctx)
	}
}

func TestApplySchema_SearchAndVectorIndexes(t *testing.T) {
	db, cleanup := setupMigratorPGWithVector(t)
	defer cleanup()
	raw := `{
		"tables": {
			"docs": {
				"validator": {
					"type": "object",
					"properties": {
						"body": { "type": "string" },
						"emb": { "type": "array", "items": { "type": "number" } }
					},
					"required": ["body"],
					"additionalProperties": false
				},
				"indexes": [],
				"searchIndexes": [
					{ "name": "by_body", "searchField": "body", "filterFields": [] }
				],
				"vectorIndexes": [
					{ "name": "by_emb", "vectorField": "emb", "dimensions": 4, "filterFields": [] }
				]
			}
		}
	}`
	if err := ApplySchema(context.Background(), db, "proj_test0005", makeSchema(t, raw)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Confirm search column and index.
	var searchIdx bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_indexes WHERE schemaname = 'nosql' AND indexname = $1)`,
		"docs_search_by_body").Scan(&searchIdx)
	if err != nil {
		t.Fatalf("query search idx: %v", err)
	}
	if !searchIdx {
		t.Error("search index docs_search_by_body not created")
	}
	var vectorIdx bool
	err = db.QueryRow(`
		SELECT EXISTS (SELECT FROM pg_indexes WHERE schemaname = 'nosql' AND indexname = $1)`,
		"docs_vector_by_emb").Scan(&vectorIdx)
	if err != nil {
		t.Fatalf("query vector idx: %v", err)
	}
	if !vectorIdx {
		t.Error("vector index docs_vector_by_emb not created")
	}
}

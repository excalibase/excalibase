//go:build integration

package schema

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// A column in two UNIQUE constraints, and a composite foreign key: the shapes
// a real schema has that broke describe_table, get_graphql_schema and
// generate_typescript_types (EXC-598).
const constraintShapesSQL = `
CREATE TABLE seasons (
	league text NOT NULL,
	year int NOT NULL,
	code text NOT NULL UNIQUE,
	PRIMARY KEY (league, year)
);
CREATE TABLE fixtures (
	id serial PRIMARY KEY,
	league text NOT NULL,
	year int NOT NULL,
	round int NOT NULL,
	slug text NOT NULL,
	UNIQUE (league, year, round),
	UNIQUE (slug),
	UNIQUE (slug, round),
	FOREIGN KEY (league, year) REFERENCES seasons (league, year)
);
GRANT ALL ON ALL TABLES IN SCHEMA public TO excalibase_app;`

func setupConstraintShapes(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	superDB, appDB, cleanup := setupPG(t)
	if _, err := superDB.Exec(constraintShapesSQL); err != nil {
		cleanup()
		t.Fatalf("setup: %v", err)
	}
	return appDB, cleanup
}

func TestIntegration_GetColumns_AColumnInTwoUniqueConstraints(t *testing.T) {
	appDB, cleanup := setupConstraintShapes(t)
	defer cleanup()

	cols, err := NewIntrospector().GetColumns(context.Background(), appDB, "public", "fixtures")
	if err != nil {
		t.Fatalf("GetColumns: %v", err)
	}
	got := map[string]ColumnInfo{}
	for _, c := range cols {
		got[c.Name] = c
	}
	if !got["slug"].Unique {
		t.Error("slug has a UNIQUE of its own and must read unique")
	}
	for _, name := range []string{"league", "year", "round"} {
		if got[name].Unique {
			t.Errorf("%s is only unique together with other columns and must not read unique", name)
		}
	}
	if !got["id"].PrimaryKey || got["slug"].PrimaryKey {
		t.Errorf("primary key flags: id %v slug %v", got["id"].PrimaryKey, got["slug"].PrimaryKey)
	}
}

func TestIntegration_GetColumns_CompositePrimaryKeyColumns(t *testing.T) {
	appDB, cleanup := setupConstraintShapes(t)
	defer cleanup()

	cols, err := NewIntrospector().GetColumns(context.Background(), appDB, "public", "seasons")
	if err != nil {
		t.Fatalf("GetColumns: %v", err)
	}
	for _, c := range cols {
		if (c.Name == "league" || c.Name == "year") && (!c.PrimaryKey || c.Unique) {
			t.Errorf("%s: primaryKey %v unique %v", c.Name, c.PrimaryKey, c.Unique)
		}
		if c.Name == "code" && !c.Unique {
			t.Error("code must read unique")
		}
	}
}

func TestIntegration_GetRelationships_CompositeForeignKeyPairsColumns(t *testing.T) {
	appDB, cleanup := setupConstraintShapes(t)
	defer cleanup()

	rels, err := NewIntrospector().GetRelationships(context.Background(), appDB, "public")
	if err != nil {
		t.Fatalf("GetRelationships: %v", err)
	}
	pairs := []string{}
	for _, r := range rels {
		if r.SourceTable == "fixtures" && r.TargetTable == "seasons" {
			pairs = append(pairs, r.SourceColumn+"->"+r.TargetColumn)
		}
	}
	if strings.Join(pairs, ",") != "league->league,year->year" {
		t.Fatalf("composite key pairs = %v", pairs)
	}
}

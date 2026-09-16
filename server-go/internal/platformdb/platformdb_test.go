package platformdb

import (
	"strings"
	"testing"
)

// The scheduler bookkeeping tables are platform metadata. The tenant's
// `public` schema is user space — fully exposed through GraphQL, REST and
// Studio — so every statement must name the reserved schema explicitly.

func TestReservedSchemaName(t *testing.T) {
	if ReservedSchema != "excalibase" {
		t.Errorf("ReservedSchema: got %q, want %q", ReservedSchema, "excalibase")
	}
}

func TestSchedulerDDL_CreatesReservedSchemaFirst(t *testing.T) {
	create := strings.Index(schedulerDDL, "CREATE SCHEMA IF NOT EXISTS excalibase")
	if create < 0 {
		t.Fatal("scheduler DDL does not create the reserved schema")
	}
	table := strings.Index(schedulerDDL, "CREATE TABLE IF NOT EXISTS excalibase.")
	if table < 0 {
		t.Fatal("scheduler DDL does not create a schema-qualified table")
	}
	if create > table {
		t.Error("scheduler DDL creates a table before the reserved schema")
	}
}

func TestSchedulerDDL_EveryObjectIsSchemaQualified(t *testing.T) {
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if !strings.Contains(schedulerDDL, "CREATE TABLE IF NOT EXISTS excalibase."+table) {
			t.Errorf("scheduler DDL does not create excalibase.%s", table)
		}
		if strings.Contains(schedulerDDL, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("scheduler DDL still creates %s unqualified", table)
		}
		if strings.Contains(schedulerDDL, "ALTER TABLE "+table) {
			t.Errorf("scheduler DDL still alters %s unqualified", table)
		}
	}
	if !strings.Contains(schedulerDDL, "ON excalibase."+scheduledFunctionsTable) {
		t.Error("scheduler DDL index is not schema-qualified")
	}
}

// The drift between the two former copies of this DDL was the cron
// `function_id` column: one copy had it, the other did not. The single
// source of truth must carry it, plus the idempotent ALTER that upgrades
// databases created before the column existed.
func TestSchedulerDDL_CarriesCronFunctionID(t *testing.T) {
	if !strings.Contains(schedulerDDL, "function_id text NOT NULL DEFAULT ''") {
		t.Error("scheduler DDL is missing the cron function_id column")
	}
	if !strings.Contains(schedulerDDL, "ADD COLUMN IF NOT EXISTS function_id") {
		t.Error("scheduler DDL is missing the idempotent function_id ALTER")
	}
}

// Existing tenants already have the tables in `public`. Every deploy must
// move them, exactly once, without failing on a fresh tenant (nothing to
// move) or on an already-migrated one (destination occupied).
func TestSchedulerDDL_MovesLegacyPublicTables(t *testing.T) {
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		move := "ALTER TABLE public." + table + " SET SCHEMA excalibase"
		if !strings.Contains(schedulerDDL, move) {
			t.Errorf("scheduler DDL is missing the migration %q", move)
		}
		if !strings.Contains(schedulerDDL, "to_regclass('public."+table+"') IS NOT NULL") {
			t.Errorf("migration of %s is not guarded on the source table existing", table)
		}
		if !strings.Contains(schedulerDDL, "to_regclass('excalibase."+table+"') IS NULL") {
			t.Errorf("migration of %s is not guarded on the destination being free", table)
		}
	}
	// The move must run before the CREATE TABLE statements, otherwise the
	// freshly created empty table occupies the destination and the legacy
	// rows are stranded in public.
	if strings.Index(schedulerDDL, "SET SCHEMA excalibase") >
		strings.Index(schedulerDDL, "CREATE TABLE IF NOT EXISTS excalibase.") {
		t.Error("scheduler DDL creates the tables before migrating the legacy ones")
	}
}

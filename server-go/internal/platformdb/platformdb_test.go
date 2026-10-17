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
	create := strings.Index(SchedulerTablesSQL, "CREATE SCHEMA IF NOT EXISTS excalibase")
	table := strings.Index(SchedulerTablesSQL, "CREATE TABLE IF NOT EXISTS excalibase.")
	if create < 0 || table < 0 || create > table {
		t.Error("scheduler DDL must create the reserved schema before its tables")
	}
}

func TestSchedulerDDL_EveryObjectIsSchemaQualified(t *testing.T) {
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if !strings.Contains(SchedulerTablesSQL, "CREATE TABLE IF NOT EXISTS excalibase."+table) {
			t.Errorf("scheduler DDL does not create excalibase.%s", table)
		}
		if strings.Contains(SchedulerTablesSQL, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("scheduler DDL still creates %s unqualified", table)
		}
		if strings.Contains(SchedulerTablesSQL, "ALTER TABLE "+table) {
			t.Errorf("scheduler DDL still alters %s unqualified", table)
		}
	}
	if !strings.Contains(SchedulerTablesSQL, "ON excalibase."+scheduledFunctionsTable) {
		t.Error("scheduler DDL index is not schema-qualified")
	}
}

// The drift between the two former copies of this DDL was the cron
// `function_id` column: one copy had it, the other did not. The single
// source of truth must carry it, plus the idempotent ALTER that upgrades
// databases created before the column existed.
func TestSchedulerDDL_CarriesCronFunctionID(t *testing.T) {
	if !strings.Contains(SchedulerTablesSQL, "function_id text NOT NULL DEFAULT ''") {
		t.Error("scheduler DDL is missing the cron function_id column")
	}
	if !strings.Contains(SchedulerTablesSQL, "ADD COLUMN IF NOT EXISTS function_id") {
		t.Error("scheduler DDL is missing the idempotent function_id ALTER")
	}
}

// A table in public is the customer's: moving it in would put a
// customer-owned object, and any trigger it carries, in the platform's schema.
func TestSchedulerDDL_NeverMovesATableInFromPublic(t *testing.T) {
	if strings.Contains(SchedulerTablesSQL, "SET SCHEMA") || strings.Contains(SchedulerTablesSQL, "public.") {
		t.Error("scheduler DDL reaches into public")
	}
}

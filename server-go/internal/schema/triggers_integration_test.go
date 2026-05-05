//go:build integration

package schema

import (
	"context"
	"database/sql"
	"testing"
)

const triggerFuncSQL = `
	CREATE OR REPLACE FUNCTION audit_trigger_func() RETURNS trigger
	LANGUAGE plpgsql AS $$
	BEGIN
		RETURN NEW;
	END;
	$$;
`

const triggerFuncErrFmt = "create trigger function: %v"
const triggerTableErrFmt = "create table: %v"

// setupTriggerDB creates a PG instance with the audit_trigger_func and trigger_test table.
func setupTriggerDB(t *testing.T) (*Introspector, *sql.DB, context.Context, func()) {
	t.Helper()
	_, appDB, cleanup := setupPG(t)
	introspector := NewIntrospector()
	ctx := context.Background()
	if _, err := appDB.ExecContext(ctx, triggerFuncSQL); err != nil {
		cleanup()
		t.Fatalf(triggerFuncErrFmt, err)
	}
	if err := introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "trigger_test", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "name", Type: "text"},
		},
	}); err != nil {
		cleanup()
		t.Fatalf(triggerTableErrFmt, err)
	}
	return introspector, appDB, ctx, cleanup
}

func TestIntegration_Triggers_Create(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupTriggerDB(t)
	defer cleanup()

	t.Run("create AFTER INSERT trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name: "trg_audit_insert", Table: "trigger_test", Schema: "public",
			Event: "INSERT", Timing: "AFTER", ForEachRow: true, Function: "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger: %v", err)
		}
		triggers, err := introspector.GetTriggers(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetTriggers: %v", err)
		}
		assertTrigger(t, triggers, "trg_audit_insert", "trigger_test", "INSERT", "AFTER", true)
	})

	t.Run("create BEFORE UPDATE trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name: "trg_before_update", Table: "trigger_test", Schema: "public",
			Event: "UPDATE", Timing: "BEFORE", ForEachRow: true, Function: "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger BEFORE UPDATE: %v", err)
		}
		assertTriggerFound(t, introspector, appDB, ctx, "trg_before_update", "BEFORE", "UPDATE")
	})

	t.Run("create statement-level trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name: "trg_statement", Table: "trigger_test", Schema: "public",
			Event: "DELETE", Timing: "AFTER", ForEachRow: false, Function: "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger statement-level: %v", err)
		}
		triggers, _ := introspector.GetTriggers(ctx, appDB, "public")
		for _, trg := range triggers {
			if trg.Name == "trg_statement" && trg.ForEachRow {
				t.Error("expected statement-level trigger (forEachRow=false)")
			}
		}
	})
}

// assertTriggerFound checks that a trigger with given name has the expected timing and event.
func assertTriggerFound(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name, timing, event string) {
	t.Helper()
	triggers, _ := i.GetTriggers(ctx, db, "public")
	for _, trg := range triggers {
		if trg.Name != name {
			continue
		}
		if trg.Timing != timing {
			t.Errorf("trigger %s: expected timing %s, got %s", name, timing, trg.Timing)
		}
		if trg.Event != event {
			t.Errorf("trigger %s: expected event %s, got %s", name, event, trg.Event)
		}
		return
	}
	t.Errorf("trigger %s not found", name)
}

func TestIntegration_Triggers_Validation(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupTriggerDB(t)
	defer cleanup()

	t.Run("invalid timing rejected", func(t *testing.T) {
		err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name: "trg_bad", Table: "trigger_test", Schema: "public",
			Event: "INSERT", Timing: "DURING", Function: "audit_trigger_func",
		})
		if err == nil {
			t.Fatal("expected error for invalid timing")
		}
	})

	t.Run("invalid event rejected", func(t *testing.T) {
		err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name: "trg_bad2", Table: "trigger_test", Schema: "public",
			Event: "MERGE", Timing: "BEFORE", Function: "audit_trigger_func",
		})
		if err == nil {
			t.Fatal("expected error for invalid event")
		}
	})
}

func TestIntegration_Triggers_Drop(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupTriggerDB(t)
	defer cleanup()

	if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
		Name: "trg_drop_me", Table: "trigger_test", Schema: "public",
		Event: "INSERT", Timing: "AFTER", ForEachRow: true, Function: "audit_trigger_func",
	}); err != nil {
		t.Fatalf("setup trigger: %v", err)
	}

	t.Run("drop trigger", func(t *testing.T) {
		if err := introspector.DropTrigger(ctx, appDB, "public", "trigger_test", "trg_drop_me"); err != nil {
			t.Fatalf("DropTrigger: %v", err)
		}
		triggers, _ := introspector.GetTriggers(ctx, appDB, "public")
		for _, trg := range triggers {
			if trg.Name == "trg_drop_me" {
				t.Error("trigger should be dropped")
			}
		}
	})
}

// assertTrigger finds a trigger by name and validates its properties.
func assertTrigger(t *testing.T, triggers []TriggerInfo, name, table, event, timing string, forEachRow bool) {
	t.Helper()
	for _, trg := range triggers {
		if trg.Name != name {
			continue
		}
		if trg.Table != table {
			t.Errorf("trigger %s: expected table %s, got %s", name, table, trg.Table)
		}
		if trg.Event != event {
			t.Errorf("trigger %s: expected event %s, got %s", name, event, trg.Event)
		}
		if trg.Timing != timing {
			t.Errorf("trigger %s: expected timing %s, got %s", name, timing, trg.Timing)
		}
		if trg.ForEachRow != forEachRow {
			t.Errorf("trigger %s: expected forEachRow=%v, got %v", name, forEachRow, trg.ForEachRow)
		}
		if !trg.Enabled {
			t.Errorf("trigger %s: should be enabled", name)
		}
		return
	}
	t.Errorf("trigger %s not found", name)
}

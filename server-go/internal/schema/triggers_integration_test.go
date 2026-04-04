//go:build integration

package schema

import (
	"context"
	"testing"
)

func TestIntegration_Triggers(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create a trigger function first (required for triggers)
	_, err := appDB.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION audit_trigger_func() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			RETURN NEW;
		END;
		$$;
	`)
	if err != nil {
		t.Fatalf("create trigger function: %v", err)
	}

	// Create a table to attach triggers to
	if err := introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "trigger_test", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "name", Type: "text"},
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("create trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name:       "trg_audit_insert",
			Table:      "trigger_test",
			Schema:     "public",
			Event:      "INSERT",
			Timing:     "AFTER",
			ForEachRow: true,
			Function:   "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger: %v", err)
		}
	})

	t.Run("get triggers", func(t *testing.T) {
		triggers, err := introspector.GetTriggers(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetTriggers: %v", err)
		}

		found := false
		for _, trg := range triggers {
			if trg.Name == "trg_audit_insert" {
				found = true
				if trg.Table != "trigger_test" {
					t.Errorf("expected table trigger_test, got %s", trg.Table)
				}
				if trg.Event != "INSERT" {
					t.Errorf("expected event INSERT, got %s", trg.Event)
				}
				if trg.Timing != "AFTER" {
					t.Errorf("expected timing AFTER, got %s", trg.Timing)
				}
				if !trg.ForEachRow {
					t.Error("expected forEachRow to be true")
				}
				if trg.Function != "audit_trigger_func" {
					t.Errorf("expected function audit_trigger_func, got %s", trg.Function)
				}
				if !trg.Enabled {
					t.Error("trigger should be enabled")
				}
			}
		}
		if !found {
			t.Error("trigger trg_audit_insert not found")
		}
	})

	t.Run("create BEFORE UPDATE trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name:       "trg_before_update",
			Table:      "trigger_test",
			Schema:     "public",
			Event:      "UPDATE",
			Timing:     "BEFORE",
			ForEachRow: true,
			Function:   "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger BEFORE UPDATE: %v", err)
		}

		triggers, _ := introspector.GetTriggers(ctx, appDB, "public")
		found := false
		for _, trg := range triggers {
			if trg.Name == "trg_before_update" {
				found = true
				if trg.Timing != "BEFORE" {
					t.Errorf("expected BEFORE, got %s", trg.Timing)
				}
				if trg.Event != "UPDATE" {
					t.Errorf("expected UPDATE, got %s", trg.Event)
				}
			}
		}
		if !found {
			t.Error("trigger trg_before_update not found")
		}
	})

	t.Run("create statement-level trigger", func(t *testing.T) {
		if err := introspector.CreateTrigger(ctx, appDB, CreateTriggerRequest{
			Name:       "trg_statement",
			Table:      "trigger_test",
			Schema:     "public",
			Event:      "DELETE",
			Timing:     "AFTER",
			ForEachRow: false,
			Function:   "audit_trigger_func",
		}); err != nil {
			t.Fatalf("CreateTrigger statement-level: %v", err)
		}

		triggers, _ := introspector.GetTriggers(ctx, appDB, "public")
		for _, trg := range triggers {
			if trg.Name == "trg_statement" {
				if trg.ForEachRow {
					t.Error("expected statement-level trigger (forEachRow=false)")
				}
			}
		}
	})

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

	t.Run("drop trigger", func(t *testing.T) {
		if err := introspector.DropTrigger(ctx, appDB, "public", "trigger_test", "trg_audit_insert"); err != nil {
			t.Fatalf("DropTrigger: %v", err)
		}

		triggers, _ := introspector.GetTriggers(ctx, appDB, "public")
		for _, trg := range triggers {
			if trg.Name == "trg_audit_insert" {
				t.Error("trigger should be dropped")
			}
		}
	})
}

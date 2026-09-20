//go:build integration

package schema

import (
	"context"
	"testing"
)

const (
	testPerfAdvisorFmt = "RunPerformanceAdvisor: %v"
	testSeverityFmt    = "expected severity high, got %s"
	testSecAdvisorFmt  = "RunSecurityAdvisor: %v"
)

func TestIntegration_AdvisorPerformance_UnindexedFK(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// The default setupPG creates orders.user_id FK without an explicit index.
	// PostgreSQL auto-creates indexes for PK/UNIQUE but NOT for FK columns.
	introspector := NewIntrospector()
	findings, err := introspector.RunPerformanceAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testPerfAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "perf-0001" {
			found = true
			if f.Severity != "high" {
				t.Errorf(testSeverityFmt, f.Severity)
			}
			if f.Category != "performance" {
				t.Errorf("expected category performance, got %s", f.Category)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected perf-0001 (unindexed FK) finding, got: %+v", findings)
	}
}

func TestIntegration_AdvisorPerformance_NoPrimaryKey(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Create a table without a primary key
	_, err := superDB.ExecContext(ctx, `CREATE TABLE no_pk_table (name text, value int)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunPerformanceAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testPerfAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "perf-0004" && f.Table == "no_pk_table" {
			found = true
			if f.Severity != "high" {
				t.Errorf(testSeverityFmt, f.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected perf-0004 (no PK) for no_pk_table, got: %+v", findings)
	}
}

func TestIntegration_AdvisorPerformance_DuplicateIndex(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Create two identical indexes on the same column
	_, err := superDB.ExecContext(ctx, `
		CREATE INDEX idx_users_email_a ON users (email);
		CREATE INDEX idx_users_email_b ON users (email);
	`)
	if err != nil {
		t.Fatalf("create indexes: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunPerformanceAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testPerfAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "perf-0009" {
			found = true
			if f.Severity != "medium" {
				t.Errorf("expected severity medium, got %s", f.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected perf-0009 (duplicate index) finding, got: %+v", findings)
	}
}

func TestIntegration_AdvisorSecurity_PolicyExistsRLSDisabled(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Create a policy on a table WITHOUT enabling RLS
	_, err := superDB.ExecContext(ctx, `
		CREATE POLICY users_policy ON users FOR SELECT USING (true);
	`)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunSecurityAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testSecAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "sec-0007" && f.Table == "users" {
			found = true
			if f.Severity != "critical" {
				t.Errorf("expected severity critical, got %s", f.Severity)
			}
			if f.Category != "security" {
				t.Errorf("expected category security, got %s", f.Category)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected sec-0007 (policy exists, RLS disabled) for users, got: %+v", findings)
	}
}

func TestIntegration_AdvisorSecurity_RLSEnabledNoPolicy(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Enable RLS without creating any policies
	_, err := superDB.ExecContext(ctx, `ALTER TABLE users ENABLE ROW LEVEL SECURITY`)
	if err != nil {
		t.Fatalf("enable RLS: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunSecurityAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testSecAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "sec-0008" && f.Table == "users" {
			found = true
			if f.Severity != "high" {
				t.Errorf(testSeverityFmt, f.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected sec-0008 (RLS enabled, no policy) for users, got: %+v", findings)
	}
}

func TestIntegration_AdvisorSecurity_RLSDisabledInPublic(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// The default tables don't have RLS enabled, so sec-0013 should fire
	introspector := NewIntrospector()
	findings, err := introspector.RunSecurityAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testSecAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "sec-0013" {
			found = true
			if f.Severity != "medium" {
				t.Errorf("expected severity medium, got %s", f.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected sec-0013 (RLS disabled in public) finding, got: %+v", findings)
	}
}

func TestIntegration_AdvisorSecurity_SecurityDefinerFunction(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Create a SECURITY DEFINER function without setting search_path
	_, err := superDB.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION insecure_func()
		RETURNS void
		LANGUAGE plpgsql
		SECURITY DEFINER
		AS $$ BEGIN NULL; END; $$;
	`)
	if err != nil {
		t.Fatalf("create function: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunSecurityAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testSecAdvisorFmt, err)
	}

	found := false
	for _, f := range findings {
		if f.RuleID == "sec-0011" {
			found = true
			if f.Severity != "high" {
				t.Errorf(testSeverityFmt, f.Severity)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected sec-0011 (function search path mutable) finding, got: %+v", findings)
	}
}

func TestIntegration_AdvisorPerformance_ReturnsAllRuleTypes(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// Create table without PK and duplicate indexes to trigger multiple rules
	_, err := superDB.ExecContext(ctx, `
		CREATE TABLE nopk (val text);
		CREATE INDEX idx_dup_a ON users (full_name);
		CREATE INDEX idx_dup_b ON users (full_name);
	`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	introspector := NewIntrospector()
	findings, err := introspector.RunPerformanceAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testPerfAdvisorFmt, err)
	}

	ruleIDs := map[string]bool{}
	for _, f := range findings {
		ruleIDs[f.RuleID] = true
		// Verify all findings have required fields
		if f.Category != "performance" {
			t.Errorf("finding %s has wrong category: %s", f.RuleID, f.Category)
		}
		if f.Title == "" {
			t.Errorf("finding %s has empty title", f.RuleID)
		}
		if f.Description == "" {
			t.Errorf("finding %s has empty description", f.RuleID)
		}
	}

	// Should have at least unindexed FK, no PK, and duplicate index
	for _, expected := range []string{"perf-0001", "perf-0004", "perf-0009"} {
		if !ruleIDs[expected] {
			t.Errorf("missing expected rule %s in findings", expected)
		}
	}
}

func TestIntegration_AdvisorSecurity_ReturnsAllRuleTypes(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	ctx := context.Background()

	// The default setup has tables without RLS -> sec-0013 should fire
	introspector := NewIntrospector()
	findings, err := introspector.RunSecurityAdvisor(ctx, superDB, "public")
	if err != nil {
		t.Fatalf(testSecAdvisorFmt, err)
	}

	for _, f := range findings {
		if f.Category != "security" {
			t.Errorf("finding %s has wrong category: %s", f.RuleID, f.Category)
		}
		if f.Title == "" {
			t.Errorf("finding %s has empty title", f.RuleID)
		}
		if f.Description == "" {
			t.Errorf("finding %s has empty description", f.RuleID)
		}
		if f.Severity == "" {
			t.Errorf("finding %s has empty severity", f.RuleID)
		}
	}

	// At minimum sec-0013 should fire since no tables have RLS
	ruleIDs := map[string]bool{}
	for _, f := range findings {
		ruleIDs[f.RuleID] = true
	}
	if !ruleIDs["sec-0013"] {
		t.Errorf("expected sec-0013 (RLS disabled) finding, got: %+v", findings)
	}
}

package service

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func fixtureTables(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("testdata/auth_migration_tables.txt")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			tables = append(tables, line)
		}
	}
	slices.Sort(tables)
	return tables
}

// EXC-821: a table the auth service creates must be on the platform's list, or
// a drifted owner is refused rather than reclaimed. The fixture is the auth
// repo's migrations; this fails loudly when the two differ.
func TestPlatformServiceSchemas_ListEveryTableTheAuthMigrationsCreate(t *testing.T) {
	var listed []string
	for _, entry := range platformServiceSchemas {
		if entry.schema == "auth" {
			listed = slices.Clone(entry.tables)
		}
	}
	slices.Sort(listed)
	if want := fixtureTables(t); !slices.Equal(listed, want) {
		t.Fatalf("auth tables listed = %v\nwant (the auth migrations) = %v", listed, want)
	}
}

func TestPlatformServiceSchemas_ListTheWatcherRegistry(t *testing.T) {
	for _, entry := range platformServiceSchemas {
		if entry.schema == "excalibase_cdc" && slices.Equal(entry.tables, []string{"excalibase_cdc_slot_owners"}) {
			return
		}
	}
	t.Fatal("excalibase_cdc does not list the watcher's slot owner registry")
}

// No repair script can run for ever: each bounds its own statements.
func TestPlatformSchemaScripts_EachBoundsItsStatements(t *testing.T) {
	lockdown, quarantine, refusal := platformSchemaScripts()
	for name, script := range map[string]string{"lockdown": lockdown, "quarantine": quarantine, "refusal": refusal} {
		if !strings.Contains(script, "SET LOCAL statement_timeout") {
			t.Errorf("the %s script has no statement_timeout", name)
		}
	}
	if strings.Contains(quarantine, "|| 'x'") {
		t.Error("the quarantine still grows a name without end")
	}
}

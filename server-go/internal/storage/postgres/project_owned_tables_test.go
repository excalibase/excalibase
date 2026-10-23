package postgres

import "testing"

func TestProjectOwnedTablesDeleteOnlyTheirOwnTableByBoundProjectID(t *testing.T) {
	for _, owned := range projectOwnedTables {
		want := "DELETE FROM " + owned.name + " WHERE project_id = $1"
		if owned.deleteSQL != want {
			t.Errorf("%s: delete statement = %q, want %q", owned.name, owned.deleteSQL, want)
		}
	}
}

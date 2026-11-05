package pgroles

import (
	"slices"
	"strings"
	"testing"
)

// The reader signs in to the project's database and nowhere else: postgres and
// template1 have no DDL trigger, and template1 would copy what it plants.
func TestReaderHBAAdmitsOnlyTheProjectDatabase(t *testing.T) {
	want := []string{
		"hostssl app " + ReaderRole + " all scram-sha-256",
		"host all " + ReaderRole + " all reject",
	}
	if got := ReaderHBA("hostssl", "app"); !slices.Equal(got, want) {
		t.Fatalf("ReaderHBA = %q, want %q", got, want)
	}
}

func TestReaderHBARejectsEverywhereForANameItCannotWrite(t *testing.T) {
	got := ReaderHBA("host", "app db,all")
	if !slices.Equal(got, []string{"host all " + ReaderRole + " all reject"}) {
		t.Fatalf("ReaderHBA = %q", got)
	}
}

// Creating a large object writes to disk whatever the transaction mode, so the
// project's database keeps it to the roles that build the schema.
func TestReaderRoleSQLTakesLargeObjectCreationFromPublic(t *testing.T) {
	sql := ReaderRoleSQL("shop_owner", "pw")
	for _, function := range largeObjectCreators {
		if !strings.Contains(sql, "REVOKE EXECUTE ON FUNCTION "+function+" FROM PUBLIC") {
			t.Errorf("%s stays open to PUBLIC", function)
		}
	}
	if strings.Contains(sql, "lo_import") {
		t.Error("server-side lo_import is superuser-only and must stay so")
	}
}

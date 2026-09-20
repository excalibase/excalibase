package config

import (
	"strings"
	"testing"
)

// DocumentDB does not load unless its libraries are preloaded, and its DDL
// path needs pg_cron. The list is upstream's own, produced by
// scripts/preload_libraries.sh for a non-distributed build at the tag the
// catalogue pins: carrying more would be guessing, carrying less does not
// start. EXC-407's image test proved exactly this list works in the image, so
// this test pins it against a well-meant edit.
func TestDocumentDBPreloadLibrariesAreUpstreamsNonDistributedList(t *testing.T) {
	want := []string{"pg_cron", "pg_documentdb_core", "pg_documentdb"}
	got := DocumentDBPreloadLibraries()

	if len(got) != len(want) {
		t.Fatalf("preload libraries: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("preload libraries: got %v, want %v", got, want)
		}
	}
}

// The returned slice is the caller's own. A caller that appends to it must
// not be able to change what the next caller is told to preload.
func TestDocumentDBPreloadLibrariesCannotBeMutatedByACaller(t *testing.T) {
	first := DocumentDBPreloadLibraries()
	first[0] = "tampered"

	if second := DocumentDBPreloadLibraries(); second[0] != "pg_cron" {
		t.Fatalf("a caller's write leaked into the next read: got %q", second[0])
	}
}

// pg_cron runs its background worker against exactly one database, named by
// cron.database_name. DocumentDB's DDL path uses pg_cron, so that setting has
// to name the database the extension is created in — the project's own
// application database, not the cluster's postgres database.
func TestDocumentDBCronDatabaseSettingIsPgCronsDatabaseName(t *testing.T) {
	if DocumentDBCronDatabaseSetting != "cron.database_name" {
		t.Fatalf("cron database setting: got %q", DocumentDBCronDatabaseSetting)
	}
}

// The extension is created by name, and CASCADE brings in what it depends on.
func TestDocumentDBExtensionIsNamedDocumentDB(t *testing.T) {
	if DocumentDBExtension != "documentdb" {
		t.Fatalf("extension name: got %q", DocumentDBExtension)
	}
}

// Every major the catalogue marks DocumentDB-capable must be one the platform
// supports, and the message an operator reads must name them all. Without
// this the API could refuse a combination while naming an empty set.
func TestDocumentDBMajorsAreAllSupportedMajors(t *testing.T) {
	supported := make(map[string]bool)
	for _, major := range PostgresMajors() {
		supported[major] = true
	}
	majors := DocumentDBMajors()
	if len(majors) == 0 {
		t.Fatal("the catalogue offers DocumentDB on no major at all")
	}
	for _, major := range majors {
		if !supported[major] {
			t.Errorf("major %s claims DocumentDB but is not a supported major", major)
		}
		if !strings.Contains(DocumentDBMajorsMessage(), major) {
			t.Errorf("major %s is missing from %q", major, DocumentDBMajorsMessage())
		}
	}
}

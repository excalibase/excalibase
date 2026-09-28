package k8s

import (
	"errors"
	"slices"
	"testing"
)

// A project's Mongo users are mapped one line each in pg_ident (EXC-427):
// the "+group" form only exists from Postgres 16, and DocumentDB is offered
// on 15.

func identOf(t *testing.T, opts PostgreSQLClusterOpts, username string, present bool) ([]string, bool, error) {
	t.Helper()
	cluster := BuildPostgreSQLCluster(opts)
	changed, updated, err := WithMongoUserIdent(cluster, username, present)
	if err != nil {
		return nil, false, err
	}
	lines, _, _ := unstructuredStrings(changed.Object, "spec", "postgresql", "pg_ident")
	before, _, _ := unstructuredStrings(cluster.Object, "spec", "postgresql", "pg_ident")
	if updated && slices.Equal(before, lines) {
		t.Error("reported a change but the lines are the same")
	}
	return lines, updated, nil
}

func TestAMongoUserGetsItsOwnPeerLine(t *testing.T) {
	lines, updated, err := identOf(t, documentDBOpts("owner_doc"), "reporting", true)
	if err != nil || !updated {
		t.Fatalf("add: updated=%v err=%v", updated, err)
	}
	want := []string{
		"local postgres documentdb_bg_worker_role",
		"local postgres documentdb",
		"local postgres owner_doc",
		"local postgres excalibase_app",
		"local postgres reporting",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("pg_ident:\n got %q\nwant %q", lines, want)
	}
}

func TestAddingAMappedUserAgainChangesNothing(t *testing.T) {
	cluster := BuildPostgreSQLCluster(documentDBOpts("owner_doc"))
	once, _, err := WithMongoUserIdent(cluster, "reporting", true)
	if err != nil {
		t.Fatal(err)
	}
	twice, updated, err := WithMongoUserIdent(once, "reporting", true)
	if err != nil || updated {
		t.Fatalf("second add: updated=%v err=%v", updated, err)
	}
	a, _, _ := unstructuredStrings(once.Object, "spec", "postgresql", "pg_ident")
	b, _, _ := unstructuredStrings(twice.Object, "spec", "postgresql", "pg_ident")
	if !slices.Equal(a, b) {
		t.Errorf("idempotent add changed lines: %q -> %q", a, b)
	}
}

func TestRemovingAUserLeavesEveryOtherLine(t *testing.T) {
	cluster := BuildPostgreSQLCluster(documentDBOpts("owner_doc"))
	withTwo, _, _ := WithMongoUserIdent(cluster, "reporting", true)
	withTwo, _, _ = WithMongoUserIdent(withTwo, "writer", true)
	removed, updated, err := WithMongoUserIdent(withTwo, "reporting", false)
	if err != nil || !updated {
		t.Fatalf("remove: updated=%v err=%v", updated, err)
	}
	lines, _, _ := unstructuredStrings(removed.Object, "spec", "postgresql", "pg_ident")
	if slices.Contains(lines, "local postgres reporting") || !slices.Contains(lines, "local postgres writer") ||
		!slices.Contains(lines, "local postgres excalibase_app") {
		t.Errorf("after remove: %q", lines)
	}
	if _, updated, _ := WithMongoUserIdent(removed, "reporting", false); updated {
		t.Error("removing an absent line reported a change")
	}
	original, _, _ := unstructuredStrings(withTwo.Object, "spec", "postgresql", "pg_ident")
	if !slices.Contains(original, "local postgres reporting") {
		t.Error("the input cluster was mutated")
	}
}

func TestOnlyADocumentDBClusterTakesMongoUserLines(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	if _, _, err := WithMongoUserIdent(BuildPostgreSQLCluster(opts), "reporting", true); !errors.Is(err, ErrClusterHasNoPeerMap) {
		t.Fatalf("plain cluster: %v", err)
	}
}

// The platform's own mappings are never removed through this path.
func TestPlatformPeerLinesCannotBeRemoved(t *testing.T) {
	for _, role := range []string{"documentdb", "documentdb_bg_worker_role", "excalibase_app", "owner_doc", ""} {
		if _, _, err := WithMongoUserIdent(BuildPostgreSQLCluster(documentDBOpts("owner_doc")), role, false); !errors.Is(err, ErrNotAMongoUserLine) {
			t.Errorf("%q: %v", role, err)
		}
	}
}

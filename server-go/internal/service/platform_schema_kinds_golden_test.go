package service

import (
	"flag"
	"os"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/schema_kinds_sql.golden")

// The generated catalog queries are part of what the repair runs as superuser,
// so a refactor of how they are built must not change one byte of them.
func TestSchemaKindQueries_AreByteIdenticalToTheGolden(t *testing.T) {
	got := ownedObjectsSQL() + "\n=====\n" + movableObjectsSQL() + "\n=====\n" + movableWithTargetsSQL() + "\n"
	const path = "testdata/schema_kinds_sql.golden"
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatal("the generated catalog queries changed; run with -update-golden only for an intended change")
	}
}

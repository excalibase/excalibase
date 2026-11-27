package planbudget

import "testing"

var freeDatabase = Size{CPUMilli: 500, MemoryBytes: 512 << 20, DiskBytes: 5 * gib}

func TestADatabaseHoldsItsSizeTimesItsCopies(t *testing.T) {
	got := ReservedDatabase(Database{Status: "ACTIVE", PerCopy: freeDatabase, Copies: 3})
	want := Size{CPUMilli: 1500, MemoryBytes: 3 * (512 << 20), DiskBytes: 15 * gib, Services: 1}
	if got != want {
		t.Fatalf("%+v want %+v", got, want)
	}
}

func TestAPausedDatabaseKeepsOnlyItsDisk(t *testing.T) {
	got := ReservedDatabase(Database{Status: "PAUSED", PerCopy: freeDatabase, Copies: 1})
	if got != (Size{DiskBytes: 5 * gib, Services: 1}) {
		t.Fatalf("%+v", got)
	}
}

func TestADatabaseThatIsGoneOrNeverWasHoldsNothing(t *testing.T) {
	for _, db := range []Database{
		{Status: "DELETING", PerCopy: freeDatabase, Copies: 1},
		{Status: "BACKUPS_PENDING_DELETE", PerCopy: freeDatabase, Copies: 1},
		{Status: "FAILED", PerCopy: freeDatabase, Copies: 1},
		{Status: "ACTIVE", PerCopy: freeDatabase, Copies: 0},
	} {
		if got := ReservedDatabase(db); !got.IsZero() {
			t.Errorf("%+v holds %+v", db, got)
		}
	}
}

func TestAnAppHoldsItsTierLimitsPerReplicaAndItsDisk(t *testing.T) {
	doc := []byte(`{"tier":"FREE","replicas":1,"status":"ACTIVE","disk":{"size":"1Gi"}}`)
	got, err := ReservedApp(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Size{CPUMilli: 250, MemoryBytes: 256 << 20, DiskBytes: gib, Services: 1}) {
		t.Fatalf("%+v", got)
	}
	three, _ := ReservedApp([]byte(`{"tier":"STANDARD","replicas":3,"status":"ACTIVE"}`))
	if three.CPUMilli != 3000 || three.MemoryBytes != 3*gib {
		t.Fatalf("%+v", three)
	}
}

func TestAStoppedAppKeepsOnlyItsDiskAndADeletedOneNothing(t *testing.T) {
	stopped, _ := ReservedApp([]byte(`{"tier":"FREE","replicas":1,"status":"PAUSED","disk":{"size":"2Gi"}}`))
	if stopped != (Size{DiskBytes: 2 * gib, Services: 1}) {
		t.Fatalf("%+v", stopped)
	}
	zero, _ := ReservedApp([]byte(`{"tier":"FREE","replicas":0,"status":"ACTIVE"}`))
	if zero != (Size{Services: 1}) {
		t.Fatalf("zero replicas %+v", zero)
	}
	gone, _ := ReservedApp([]byte(`{"tier":"FREE","replicas":1,"status":"DELETING","disk":{"size":"2Gi"}}`))
	if !gone.IsZero() {
		t.Fatalf("%+v", gone)
	}
}

func TestAnUnreadableAppIsAnErrorNotZero(t *testing.T) {
	for _, doc := range []string{`not json`, `{"tier":"NOPE","replicas":1,"status":"ACTIVE"}`, `{"tier":"FREE","replicas":1,"status":"ACTIVE","disk":{"size":"lots"}}`} {
		if _, err := ReservedApp([]byte(doc)); err == nil {
			t.Errorf("%s was read as a size", doc)
		}
	}
}

func TestTheFunctionsRuntimeHoldsOneSmallPodAndNeverNothing(t *testing.T) {
	if ReservedFunctions("FREE") != (Size{CPUMilli: 200, MemoryBytes: 128 << 20, Services: 1}) {
		t.Fatal("free")
	}
	if ReservedFunctions("MYSTERY").Services != 1 {
		t.Fatal("an unknown plan still holds the smallest pod")
	}
}

func TestTheSameRecordIsAdmittedAsRunningWhenItIsAboutToRun(t *testing.T) {
	doc := []byte(`{"tier":"FREE","replicas":2,"status":"PAUSED"}`)
	now, _ := ReservedApp(doc)
	running, err := ReservedAppAs(doc, "ACTIVE")
	if err != nil || now.CPUMilli != 0 || running.CPUMilli != 500 {
		t.Fatalf("paused %+v, as running %+v, %v", now, running, err)
	}
}

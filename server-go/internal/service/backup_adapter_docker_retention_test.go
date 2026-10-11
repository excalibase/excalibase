package service

import (
	"context"
	"testing"
	"time"
)

// EXC-593: a backup notes the WAL segment it starts in, so retention knows
// which archived WAL a kept backup still replays.
func TestDockerBackupNotesTheSegmentItStartsIn(t *testing.T) {
	adapter, store, _, uploader, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{walName: "000000010000000000000007"})
	inst, _ := store.FindByProjectID("dk-1")
	ref, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(uploader.objects["test-backups/backups/dk-1/manual/"+ref.ID+".wal"]); got != "000000010000000000000007" {
		t.Fatalf("start segment %q", got)
	}
}

// Retention keeps every backup a point inside the window needs, the last one
// before the window included, and the WAL they replay; timeline histories
// stay. The rest goes, with its records.
func TestDockerRetentionDropsWhatTheWindowNoLongerReaches(t *testing.T) {
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	now := time.Now()
	seedDockerBackups(t, uploader, records, map[string]time.Time{
		"backup-a": now.Add(-10 * 24 * time.Hour), "backup-b": now.Add(-8 * 24 * time.Hour), "backup-c": now.Add(-2 * 24 * time.Hour),
	})
	starts := map[string]string{"backup-a": "000000010000000000000002", "backup-b": "000000010000000000000005", "backup-c": "000000020000000000000009"}
	for id, segment := range starts {
		uploader.objects["test-backups/backups/dk-1/manual/"+id+".wal"] = []byte(segment)
	}
	for _, name := range []string{"000000010000000000000003", "000000010000000000000006", "00000002.history", "000000020000000000000009"} {
		uploader.objects["test-backups/backups/dk-1/wals/"+name+".gz"] = []byte("w")
	}
	inst, _ := store.FindByProjectID("dk-1")
	seven := 7
	inst.BackupRetentionDays = &seven
	if _, err := adapter.PurgeExpired(context.Background(), inst); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	for key, kept := range map[string]bool{
		"manual/backup-a.tar.gz": false, "manual/backup-a.wal": false,
		"manual/backup-b.tar.gz": true, "manual/backup-c.tar.gz": true,
		"wals/000000010000000000000003.gz": false, "wals/000000010000000000000006.gz": true,
		"wals/00000002.history.gz": true, "wals/000000020000000000000009.gz": true,
	} {
		if _, ok := uploader.objects["test-backups/backups/dk-1/"+key]; ok != kept {
			t.Errorf("%s kept=%v, want %v", key, ok, kept)
		}
	}
	left, _ := records.ListByProject(context.Background(), "dk-1")
	if len(left) != 2 {
		t.Fatalf("records left %v", left)
	}
}

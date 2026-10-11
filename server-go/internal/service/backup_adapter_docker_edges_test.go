package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func gzipped(data string) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, _ = writer.Write([]byte(data))
	_ = writer.Close()
	return buf.Bytes()
}

// A point-in-time copy gets the archived WAL its recovery replays.
func TestDockerRestoreHandsTheArchivedWALToRecovery(t *testing.T) {
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	dc := &fakeDockerClientForAdapter{}
	adapter.SetDockerClient(dc)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	seedDockerBackups(t, uploader, records, map[string]time.Time{"backup-old": time.Now().Add(-time.Hour)})
	uploader.objects["test-backups/backups/dk-1/wals/"+segment+".gz"] = gzipped("wal")
	src, _ := store.FindByProjectID("dk-1")
	point := domain.ZonedTime{Time: time.Now().Add(-time.Minute)}
	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "c", TargetProjectID: "c", TargetTime: &point}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if dc.copyCalls < 3 {
		t.Fatalf("base, WAL and recovery settings expected, got %d copies", dc.copyCalls)
	}
	uploader.objects["test-backups/backups/dk-1/wals/"+segment+".gz"] = []byte("not gzip")
	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "d", TargetProjectID: "d", TargetTime: &point}); err == nil {
		t.Fatal("a corrupt WAL segment was accepted")
	}
}

func TestDockerInPlaceReplaceRefusesWhatItCannotDo(t *testing.T) {
	f := newInPlaceFixture(t)
	ctx := context.Background()
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, map[string]interface{}{}); !errors.Is(err, errInPlaceTargetMalformed) {
		t.Fatalf("malformed target: %v", err)
	}
	f.dc.failOn = "copyFrom"
	target, _ := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{})
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err == nil || len(f.dc.removed) != 0 {
		t.Fatalf("replaced a database whose last WAL never shipped: %v, removed %v", err, f.dc.removed)
	}
	f.adapter.SetDockerClient(nil)
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err == nil {
		t.Fatal("replaced without an engine")
	}
}

func TestDockerRetentionNeedsTheProjectsRetention(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")
	if _, err := adapter.PurgeExpired(context.Background(), inst); err == nil {
		t.Fatal("purged without knowing the retention")
	}
}

func TestHostPostgresWALSweepRunsUntilStopped(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewHostPostgresWALSweep(adapter, store).Run(ctx, time.Hour)
}

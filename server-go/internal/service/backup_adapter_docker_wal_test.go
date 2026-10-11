package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const segment = "000000010000000000000003"

func gunzipped(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(reader)
	return string(out)
}

// EXC-593: closed segments leave the container for the store and are removed
// from it once there; a segment still being copied, or a file that is no WAL,
// is neither shipped nor removed.
func TestDockerAdapterShipsClosedWALAndClearsWhatItShipped(t *testing.T) {
	adapter, store, _, uploader, _ := setupDockerAdapter(t)
	dc := &fakeDockerClientForAdapter{walArchive: map[string][]byte{
		segment:            []byte("wal three"),
		"00000002.history": []byte("1\t0/3000000\tno reason"),
		segment + ".part":  []byte("half"),
		"notes.txt":        []byte("not wal"),
	}}
	adapter.SetDockerClient(dc)
	inst, _ := store.FindByProjectID("dk-1")
	if err := adapter.ShipWAL(context.Background(), inst); err != nil {
		t.Fatalf("ShipWAL: %v", err)
	}
	if got := gunzipped(t, uploader.objects["test-backups/backups/dk-1/wals/"+segment+".gz"]); got != "wal three" {
		t.Fatalf("segment shipped as %q", got)
	}
	if _, ok := uploader.objects["test-backups/backups/dk-1/wals/00000002.history.gz"]; !ok {
		t.Fatal("timeline history not shipped")
	}
	if len(uploader.objects) != 2 {
		t.Fatalf("shipped %d objects", len(uploader.objects))
	}
	if _, left := dc.walArchive[segment]; left || len(dc.walArchive) != 2 {
		t.Fatalf("left in the container: %v", dc.walArchive)
	}
}

// A project with backups and no archive directory is not reported as backed up.
func TestDockerAdapterShipWALFailsWithoutAnArchive(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{failOn: "copyFrom"})
	inst, _ := store.FindByProjectID("dk-1")
	if err := adapter.ShipWAL(context.Background(), inst); err == nil {
		t.Fatal("shipped nothing and reported success")
	}
}

func TestHostPostgresWALSweepShipsOnlyRunningBackedUpPostgres(t *testing.T) {
	adapter, store, _, uploader, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{walArchive: map[string][]byte{segment: []byte("w")}})
	on := true
	project := func(id, status string, engine domain.DatabaseType, mode domain.DeploymentMode, backups *bool) {
		_ = store.Create(&domain.DatabaseInstance{ProjectID: id, OrgID: "org", Namespace: id, Status: status,
			DBType: engine, DeploymentMode: mode, BackupEnabled: backups})
	}
	project("live", "ACTIVE", domain.PostgreSQL, domain.ModeDocker, &on)
	project("paused", "PAUSED", domain.PostgreSQL, domain.ModeDocker, &on)
	project("nobackups", "ACTIVE", domain.PostgreSQL, domain.ModeDocker, nil)
	project("cluster", "ACTIVE", domain.PostgreSQL, domain.ModeK8s, &on)
	project("mysql", "ACTIVE", domain.MySQL, domain.ModeDocker, &on)
	NewHostPostgresWALSweep(adapter, store).Sweep(context.Background())
	if len(uploader.objects) != 1 {
		t.Fatalf("shipped %v", uploader.objects)
	}
	if _, ok := uploader.objects["test-backups/backups/live/wals/"+segment+".gz"]; !ok {
		t.Fatalf("live project not shipped: %v", uploader.objects)
	}
}

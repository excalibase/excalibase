package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-587: every way a ship or a replay cannot go on is an error, never a
// window that claims more than was shipped.

func TestMySQLCommitStateIsReadStrictly(t *testing.T) {
	for name, out := range map[string]string{
		"two fields":  serverA + "\t2026-10-11T13:00:00Z\n",
		"short uuid":  "abc\t2026-10-11T13:00:00Z\t\n",
		"bad time":    serverA + "\tyesterday\t" + serverA + ":1-3\n",
		"empty":       "",
		"extra field": serverA + "\t2026-10-11T13:00:00Z\tx\ty\n",
	} {
		if _, err := parseMySQLCommitState(out); err == nil {
			t.Errorf("%s: %q was accepted", name, out)
		}
	}
	state, err := parseMySQLCommitState(serverA + "\t2026-10-11T13:00:00Z\t\n")
	if err != nil || state.GTIDs != "" || !state.At.Equal(at(13, 0)) {
		t.Fatalf("a server that committed nothing: %+v %v", state, err)
	}
}

func TestShipMarksThatCannotBeReadAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     "{",
		"no next file": `{"through":"2026-10-11T13:00:00Z"}`,
		"no time":      `{"nextBinlog":"binlog.000004"}`,
	} {
		if _, err := decodeShipMark(strings.NewReader(body), "k"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	store := &fakeShipStore{fakeObjectDeleter: newFakeObjectDeleter()}
	if _, err := readMySQLShipMark(context.Background(), store, "b", "missing"); err == nil {
		t.Error("a missing mark was read")
	}
	_, _ = store.Upload(context.Background(), "b", "backwards", strings.NewReader(
		`{"firstBinlog":"binlog.000009","nextBinlog":"binlog.000004","through":"2026-10-11T13:00:00Z"}`))
	if _, err := readMySQLShipMark(context.Background(), store, "b", "backwards"); err == nil {
		t.Error("a mark whose files run backwards was read")
	}
}

func TestMySQLShipFailuresWriteNoMark(t *testing.T) {
	cases := map[string]func(*mysqlShipLab){
		"no exec":    func(lab *mysqlShipLab) { lab.adapter.streamer = nil },
		"no cluster": func(lab *mysqlShipLab) { delete(lab.mock.CRDs, sourceClusterKey) },
		"store read only": func(lab *mysqlShipLab) {
			lab.adapter.openStore = func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return lab.objects, nil }
		},
		"purged binlog":    func(lab *mysqlShipLab) { lab.pods.oldest = 5 },
		"exec fails":       func(lab *mysqlShipLab) { lab.pods.failOn = "cat" },
		"strange gtid set": func(lab *mysqlShipLab) { lab.pods.gtid = "x'; DROP TABLE t; --" },
		"strange binlog dir": func(lab *mysqlShipLab) {
			lab.pods.basename = "/var/lib/mysql/$(reboot)/binlog"
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			lab := newMySQLShipLab(t)
			breakIt(lab)
			if err := lab.adapter.ShipBinlogs(context.Background(), lab.source); err == nil {
				t.Fatal("want an error")
			}
			if containsKey(lab.store.remaining(), lab.shipped()+"shipped.json") {
				t.Fatal("a failed ship wrote its mark")
			}
		})
	}
}

func TestMySQLRestorePastTheBackupNeedsEveryShippedFile(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	missing := lab.shipped() + "binlog.000004.gz"
	if err := lab.store.DeleteKeys(context.Background(), "b", []string{missing}); err != nil {
		t.Fatal(err)
	}
	err := lab.adapter.CheckRestoreTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(12, 30))})
	if err != nil {
		t.Fatalf("the window still reaches 12:30 by the mark: %v", err)
	}
	lab.restoreReady(t)
	_, err = lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{TargetTime: zoned(at(12, 30))}))
	if !errors.Is(err, ErrRestoreTargetInvalid) || !strings.Contains(err.Error(), "binlog.000004") {
		t.Fatalf("a restore without binlog.000004: %v", err)
	}
	if _, created := lab.mock.CRDs[restoredNS+"/"+k8s.MySQLClusterName(restoredProject)]; created {
		t.Fatal("a cluster was created for a restore that cannot finish")
	}
}

func TestMySQLRestoreAtPlansWithoutABaseOrAMark(t *testing.T) {
	backups := readMySQLBackups(sourceKeys(), k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject))
	if _, _, err := mysqlRestoreAt(backups, nil, at(0, 0).Add(-time.Hour)); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Errorf("before the first backup: %v", err)
	}
	if _, _, err := mysqlRestoreAt(backups, nil, at(12, 30)); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Errorf("past the newest backup with no ship: %v", err)
	}
	point, tail, err := mysqlRestoreAt(backups, nil, at(12, 0))
	if err != nil || tail != nil || !point.Equal(at(12, 0)) {
		t.Errorf("a backup's own time: %v %v %v", point, tail, err)
	}
}

func TestMySQLCatalogRefusesAMarkOfAnotherRun(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	key := lab.shipped() + "shipped.json"
	lab.store.data[key] = []byte(strings.Replace(string(lab.store.data[key]), "20261011-120000", "20261011-060000", 1))
	if _, err := lab.adapter.RestoreWindow(context.Background(), lab.source); err == nil {
		t.Fatal("a mark naming another run was trusted")
	}
}

func TestMySQLReplayFailuresAreReported(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.ship(t)
	backups, marks, err := lab.adapter.catalog(context.Background(), lab.source)
	if err != nil {
		t.Fatal(err)
	}
	_, tail, err := mysqlRestoreAt(backups, marks, at(12, 30))
	if err != nil || tail == nil {
		t.Fatalf("tail %v %v", tail, err)
	}
	if err := lab.adapter.replayTail(context.Background(), mysqlSourceNS, mysqlSourceProject, nil); err != nil {
		t.Fatalf("no tail is nothing to do: %v", err)
	}
	lab.pods.failOn = "mysqlbinlog"
	if err := lab.adapter.replayTail(context.Background(), mysqlSourceNS, mysqlSourceProject, tail); err == nil {
		t.Fatal("a failed replay was reported done")
	}
	lab.adapter.streamer = nil
	if err := lab.adapter.replayTail(context.Background(), mysqlSourceNS, mysqlSourceProject, tail); err == nil {
		t.Fatal("a replay without exec was reported done")
	}
	unsized := &mysqlTail{files: []StoredObject{{Key: "k/binlog.000004.gz"}}, stopAt: at(12, 30)}
	if err := writeMySQLTailArchive(context.Background(), lab.store, "b", unsized.files, io.Discard); err == nil {
		t.Fatal("a file of unknown size was archived")
	}
}

func TestBinlogSweepsRunUntilTheirContextEnds(t *testing.T) {
	lab := newMySQLShipLab(t)
	store := emptyInstanceStore(t)
	if err := store.Create(lab.source); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewMySQLBinlogSweep(lab.adapter, store, NewLeadership(AlwaysLeader{})).Run(ctx, time.Hour)
	if !containsKey(lab.store.remaining(), lab.shipped()+"shipped.json") {
		t.Fatal("the sweep did not ship before it stopped")
	}
	host := newShipFixture(t)
	host.tick(time.Minute)
	host.commit()
	NewHostMySQLBinlogSweep(host.adapter, host.adapter.instances).Run(ctx, time.Hour)
	if keys := host.keys("backups/p1/mysql/binlogs/" + serverA + "/shipped.json"); len(keys) != 1 {
		t.Fatal("the single-host sweep did not ship before it stopped")
	}
}

func TestBinlogSweepsLogAndGoOnWhenAProjectFails(t *testing.T) {
	lab := newMySQLShipLab(t)
	lab.pods.lagging = true
	store := emptyInstanceStore(t)
	if err := store.Create(lab.source); err != nil {
		t.Fatal(err)
	}
	NewMySQLBinlogSweep(lab.adapter, store, NewLeadership(erroringLock{err: errors.New("down")})).Sweep(context.Background())
	NewMySQLBinlogSweep(lab.adapter, store, NewLeadership(AlwaysLeader{})).Sweep(context.Background())
	if lab.pods.flushes != 0 {
		t.Fatal("a lagging source was flushed")
	}
	host := newShipFixture(t)
	host.engine.containers["excalibase-p1-mysql"] = true
	host.adapter.uploader = nil
	NewHostMySQLBinlogSweep(host.adapter, host.adapter.instances).Sweep(context.Background())
}

func TestHostMySQLShipErrorsWriteNoMark(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(time.Minute)
	f.commit()
	f.store.failOn = "backups/p1/mysql/binlogs/"
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err == nil {
		t.Fatal("a failed upload was reported shipped")
	}
	f.store.failOn = ""
	if keys := f.keys("backups/p1/mysql/binlogs/" + serverA + "/shipped.json"); len(keys) != 0 {
		t.Fatalf("a failed ship wrote its mark: %v", keys)
	}
	f.adapter.uploader = nil
	if err := f.adapter.ShipBinlogs(ctx, f.inst); !errors.Is(err, ErrBackupStorageNotConfigured) {
		t.Fatalf("no store: %v", err)
	}
}

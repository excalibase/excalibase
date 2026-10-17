package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const hostRuns = "backups/p1/mysql/runs/"

const (
	serverA = "11111111-1111-1111-1111-111111111111"
	serverB = "22222222-2222-2222-2222-222222222222"
)

// Server A backed up at 00:00, 06:00 and 08:00 (the safety backup of an
// in-place restore), then server B, its replacement, at 08:30. The 12:00 run
// of B never wrote its manifest.
func hostCatalogObjects() []S3Object {
	run := func(id, server string, size int64) []S3Object {
		dir := hostRuns + id + "-" + server + "/"
		return []S3Object{{Key: dir + hostMySQLDumpFile, SizeBytes: size}, {Key: dir + hostMySQLManifestFile, SizeBytes: 10}}
	}
	var objects []S3Object
	objects = append(objects, run("20261011-000000", serverA, 100)...)
	objects = append(objects, run("20261011-060000", serverA, 200)...)
	objects = append(objects, run("20261011-080000", serverA, 300)...)
	objects = append(objects, run("20261011-083000", serverB, 400)...)
	objects = append(objects, S3Object{Key: hostRuns + "20261011-120000-" + serverB + "/" + hostMySQLDumpFile, SizeBytes: 5})
	objects = append(objects, S3Object{Key: hostRuns + "not-a-run/x"})
	return objects
}

func TestHostMySQLRunsAreReadFromTheirKeys(t *testing.T) {
	runs := readHostMySQLRuns(hostCatalogObjects(), hostRuns)
	if len(runs) != 5 {
		t.Fatalf("runs %+v", runs)
	}
	first := runs[0]
	if first.ID != "20261011-000000" || first.Server != serverA || !first.At.Equal(at(0, 0)) || !first.Complete || first.Size != 110 || len(first.Keys) != 2 {
		t.Fatalf("first %+v", first)
	}
	if runs[4].Complete {
		t.Fatal("a run without its manifest is not complete")
	}
	if completed := completedHostMySQLRuns(runs); len(completed) != 4 {
		t.Fatalf("completed %d", len(completed))
	}
	window, ok := hostMySQLWindow(runs, nil)
	if !ok || !window.Earliest.Equal(at(0, 0)) || !window.Latest.Equal(at(8, 30)) {
		t.Fatalf("window %+v %v", window, ok)
	}
}

func TestHostMySQLRestorePointNeedsTheSameServersLaterRun(t *testing.T) {
	runs := readHostMySQLRuns(hostCatalogObjects(), hostRuns)
	at := func(hour, minute int) *domain.ZonedTime {
		return &domain.ZonedTime{Time: time.Date(2026, 10, 11, hour, minute, 7, 500, time.UTC)}
	}
	point, err := resolveHostMySQLPoint(runs, nil, domain.RestoreRequest{TargetTime: at(3, 0)})
	if err != nil || point.Base.ID != "20261011-000000" || point.Cover.ID != "20261011-060000" || !point.Replay ||
		!point.Point.Equal(time.Date(2026, 10, 11, 3, 0, 7, 0, time.UTC)) {
		t.Fatalf("03:00 → %+v %v", point, err)
	}
	if point, err = resolveHostMySQLPoint(runs, nil, domain.RestoreRequest{TargetTime: at(8, 10)}); err == nil {
		t.Fatalf("08:10 lies after A's last run and before B's first; got %+v", point)
	} else if !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Fatalf("08:10: %v", err)
	}
	if _, err = resolveHostMySQLPoint(runs, nil, domain.RestoreRequest{TargetTime: at(9, 0)}); err == nil {
		t.Fatal("09:00 is after the last complete run")
	}
	point, err = resolveHostMySQLPoint(runs, nil, domain.RestoreRequest{BackupID: "20261011-083000"})
	if err != nil || point.Base.Server != serverB || point.Replay {
		t.Fatalf("a backup id restores its dump alone: %+v %v", point, err)
	}
	if point, err = resolveHostMySQLPoint(runs, nil, domain.RestoreRequest{}); err != nil || point.Base.ID != "20261011-083000" {
		t.Fatalf("no target takes the newest backup: %+v %v", point, err)
	}
	for _, req := range []domain.RestoreRequest{{BackupID: "20261011-120000"}, {TargetXID: "42"}, {TargetLSN: "0/1"}, {TargetName: "x"}} {
		if _, err := resolveHostMySQLPoint(runs, nil, req); !errors.Is(err, ErrRestoreTargetInvalid) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	if _, err := resolveHostMySQLPoint(nil, nil, domain.RestoreRequest{}); err == nil || !strings.Contains(err.Error(), "no completed backup") {
		t.Fatalf("no runs: %v", err)
	}
}

func TestHostMySQLRetentionKeepsTheRunThatReachesTheCutoff(t *testing.T) {
	runs := completedHostMySQLRuns(readHostMySQLRuns(hostCatalogObjects(), hostRuns))
	expired := expiredHostMySQLRuns(runs, at(7, 0))
	if len(expired) != 1 || expired[0].ID != "20261011-000000" {
		t.Fatalf("expired %+v", expired)
	}
	if expired := expiredHostMySQLRuns(runs, at(23, 0)); len(expired) != 3 {
		t.Fatalf("the newest run is never expired: %+v", expired)
	}
}

func TestBinlogIndexesAreReadAndListed(t *testing.T) {
	if index, err := binlogIndex("binlog.000123"); err != nil || index != 123 {
		t.Fatalf("index %d %v", index, err)
	}
	for _, bad := range []string{"binlog.index", "relay.000001", "binlog.", "../binlog.000001"} {
		if _, err := binlogIndex(bad); err == nil {
			t.Errorf("%q read as a binlog", bad)
		}
	}
	files, err := binlogRange("binlog.000007", "binlog.000010")
	if err != nil || strings.Join(files, ",") != "binlog.000007,binlog.000008,binlog.000009" {
		t.Fatalf("range %v %v", files, err)
	}
	if files, err := binlogRange("binlog.000007", "binlog.000007"); err != nil || len(files) != 0 {
		t.Fatalf("empty range %v %v", files, err)
	}
	if _, err := binlogRange("binlog.000009", "binlog.000007"); err == nil {
		t.Fatal("a range that runs backwards was accepted")
	}
}

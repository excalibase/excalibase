package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const catalogPrefix = "moco/org1-proj1/proj1-mysql/"

// Three runs: the first a dump only; each later run adds the binlogs since
// the previous one into that previous run's directory (MOCO backup.go:440).
func catalogKeys() []string {
	return []string{
		catalogPrefix + "20261011-000000/binlog.tar.zst",
		catalogPrefix + "20261011-000000/dump.tar",
		catalogPrefix + "20261011-060000/binlog.tar.zst",
		catalogPrefix + "20261011-060000/dump.tar",
		catalogPrefix + "20261011-120000/dump.tar",
		catalogPrefix + "garbage.txt",
	}
}

func at(hour, minute int) time.Time { return time.Date(2026, 10, 11, hour, minute, 0, 0, time.UTC) }

func TestReadMySQLBackups(t *testing.T) {
	backups := readMySQLBackups(catalogKeys(), catalogPrefix)
	if len(backups) != 3 {
		t.Fatalf("backups %+v", backups)
	}
	if backups[0].ID != "20261011-000000" || !backups[0].At.Equal(at(0, 0)) || !backups[0].Binlog || len(backups[0].Keys) != 2 {
		t.Errorf("first %+v", backups[0])
	}
	if backups[2].Binlog {
		t.Error("the newest run has no binlogs yet")
	}
	window, ok := mysqlRestoreWindow(backups)
	if !ok || !window.Earliest.Equal(at(0, 0)) || !window.Latest.Equal(at(12, 0)) {
		t.Errorf("window %+v %v", window, ok)
	}
	if _, ok := mysqlRestoreWindow(nil); ok {
		t.Error("a window without backups")
	}
}

func zoned(t time.Time) *domain.ZonedTime { return &domain.ZonedTime{Time: t} }

// Owner decision: time targets only, inside the window; blank = the newest
// completed backup.
func TestResolveMySQLRestorePoint(t *testing.T) {
	backups := readMySQLBackups(catalogKeys(), catalogPrefix)
	for name, tc := range map[string]struct {
		req  domain.RestoreRequest
		want time.Time
	}{
		"latest":         {domain.RestoreRequest{}, at(12, 0)},
		"a backup":       {domain.RestoreRequest{BackupID: "20261011-060000"}, at(6, 0)},
		"between":        {domain.RestoreRequest{TargetTime: zoned(at(7, 30))}, at(7, 30)},
		"the earliest":   {domain.RestoreRequest{TargetTime: zoned(at(0, 0))}, at(0, 0)},
		"the newest":     {domain.RestoreRequest{TargetTime: zoned(at(12, 0))}, at(12, 0)},
		"sub-second off": {domain.RestoreRequest{TargetTime: zoned(at(7, 30).Add(400 * time.Millisecond))}, at(7, 30)},
	} {
		got, err := resolveMySQLRestorePoint(backups, tc.req)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: %v %v, want %v", name, got, err, tc.want)
		}
	}
}

func TestResolveMySQLRestorePoint_Refusals(t *testing.T) {
	backups := readMySQLBackups(catalogKeys(), catalogPrefix)
	for name, tc := range map[string]struct {
		req      domain.RestoreRequest
		backups  []mysqlBackup
		contains string
	}{
		"xid":          {domain.RestoreRequest{TargetXID: "42"}, backups, "time"},
		"lsn":          {domain.RestoreRequest{TargetLSN: "0/1"}, backups, "time"},
		"name":         {domain.RestoreRequest{TargetName: "before"}, backups, "time"},
		"before":       {domain.RestoreRequest{TargetTime: zoned(at(0, 0).Add(-time.Minute))}, backups, "2026-10-11T00:00:00Z – 2026-10-11T12:00:00Z"},
		"after":        {domain.RestoreRequest{TargetTime: zoned(at(12, 1))}, backups, "backup window"},
		"unknown id":   {domain.RestoreRequest{BackupID: "20261011-030000"}, backups, "20261011-030000"},
		"none yet":     {domain.RestoreRequest{}, nil, "no completed backup"},
		"binlogs lost": {domain.RestoreRequest{TargetTime: zoned(at(3, 0))}, withoutBinlog(backups, 0), "binlogs"},
	} {
		_, err := resolveMySQLRestorePoint(tc.backups, tc.req)
		if !errors.Is(err, ErrRestoreTargetInvalid) || !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: %v, want an invalid target naming %q", name, err, tc.contains)
		}
	}
}

func withoutBinlog(backups []mysqlBackup, index int) []mysqlBackup {
	out := append([]mysqlBackup(nil), backups...)
	out[index].Binlog = false
	return out
}

// Retention deletes whole runs older than the cutoff but keeps the newest one
// at or before it, so the window still reaches back to the cutoff, and never
// the newest run.
func TestExpiredMySQLBackups(t *testing.T) {
	backups := readMySQLBackups(catalogKeys(), catalogPrefix)
	expired := expiredMySQLBackups(backups, at(7, 0))
	if len(expired) != 1 || expired[0].ID != "20261011-000000" {
		t.Errorf("expired %+v", expired)
	}
	if expired := expiredMySQLBackups(backups, at(23, 0)); len(expired) != 2 {
		t.Errorf("all but the newest: %+v", expired)
	}
	if expired := expiredMySQLBackups(backups, at(0, 0).Add(-time.Hour)); len(expired) != 0 {
		t.Errorf("nothing is old enough: %+v", expired)
	}
}

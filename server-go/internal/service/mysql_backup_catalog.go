package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A MySQL project's backups are what MOCO wrote under its prefix: one
// directory per run named by its time, holding the dump of that run and,
// once the next run has happened, the binlogs from this run to that one.
// So a point can be restored only between the first and the last run.

// ErrRestoreTargetInvalid marks a restore target the project's backups
// cannot reach; the message says what can.
var ErrRestoreTargetInvalid = errors.New("invalid restore target")

// RestoreTargetError is a refused restore target, written for the caller.
type RestoreTargetError struct{ Message string }

func (e *RestoreTargetError) Error() string { return e.Message }

func (e *RestoreTargetError) Is(target error) bool { return target == ErrRestoreTargetInvalid }

// RestoreWindow is the span a point-in-time restore can reach.
type RestoreWindow struct {
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
}

func (w RestoreWindow) String() string {
	return "point-in-time within the backup window: " + w.Earliest.UTC().Format(time.RFC3339) + " – " + w.Latest.UTC().Format(time.RFC3339)
}

// mysqlBackup is one MOCO run.
type mysqlBackup struct {
	ID     string
	At     time.Time
	Dump   bool
	Binlog bool
	Keys   []string
}

// readMySQLBackups groups the keys under prefix into runs, oldest first.
// Anything not in a run's directory is ignored, as MOCO's restore does.
func readMySQLBackups(keys []string, prefix string) []mysqlBackup {
	runs := map[string]*mysqlBackup{}
	for _, key := range keys {
		directory, file, ok := strings.Cut(strings.TrimPrefix(key, prefix), "/")
		if !ok || strings.Contains(file, "/") {
			continue
		}
		when, err := time.Parse(k8s.MOCOBackupTimeFormat, directory)
		if err != nil {
			continue
		}
		run, seen := runs[directory]
		if !seen {
			run = &mysqlBackup{ID: directory, At: when}
			runs[directory] = run
		}
		run.Keys = append(run.Keys, key)
		run.Dump = run.Dump || file == k8s.MOCODumpFile
		run.Binlog = run.Binlog || file == k8s.MOCOBinlogFile
	}
	backups := make([]mysqlBackup, 0, len(runs))
	for _, run := range runs {
		backups = append(backups, *run)
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].At.Before(backups[j].At) })
	return backups
}

// completedMySQLBackups are the runs whose dump is in the bucket.
func completedMySQLBackups(backups []mysqlBackup) []mysqlBackup {
	completed := make([]mysqlBackup, 0, len(backups))
	for _, backup := range backups {
		if backup.Dump {
			completed = append(completed, backup)
		}
	}
	return completed
}

func mysqlRestoreWindow(backups []mysqlBackup) (RestoreWindow, bool) {
	completed := completedMySQLBackups(backups)
	if len(completed) == 0 {
		return RestoreWindow{}, false
	}
	return RestoreWindow{Earliest: completed[0].At, Latest: completed[len(completed)-1].At}, true
}

// resolveMySQLRestorePoint is the time a restore asks for: a backup's own
// time, a time inside the window, or the newest backup when nothing is named.
func resolveMySQLRestorePoint(backups []mysqlBackup, req domain.RestoreRequest) (time.Time, error) {
	if req.TargetXID != "" || req.TargetLSN != "" || req.TargetName != "" {
		return time.Time{}, &RestoreTargetError{"a MySQL project restores to a time only; transaction ids, LSNs and restore point names are Postgres targets"}
	}
	completed := completedMySQLBackups(backups)
	window, ok := mysqlRestoreWindow(completed)
	if !ok {
		return time.Time{}, &RestoreTargetError{"this project has no completed backup to restore from yet"}
	}
	switch {
	case req.BackupID != "":
		for _, backup := range completed {
			if backup.ID == req.BackupID {
				return backup.At, nil
			}
		}
		return time.Time{}, &RestoreTargetError{fmt.Sprintf("backup %s is not one of this project's completed backups", req.BackupID)}
	case req.TargetTime != nil:
		return pointInWindow(completed, window, req.TargetTime.Time)
	}
	return window.Latest, nil
}

// pointInWindow accepts a time MOCO can reach: a run's own time, or a time
// after a run whose binlogs reach to the next one. MOCO reads it to the second.
func pointInWindow(completed []mysqlBackup, window RestoreWindow, wanted time.Time) (time.Time, error) {
	point := wanted.UTC().Truncate(time.Second)
	if point.Before(window.Earliest) || point.After(window.Latest) {
		return time.Time{}, &RestoreTargetError{fmt.Sprintf("%s is outside the %s", point.Format(time.RFC3339), window)}
	}
	var base mysqlBackup
	for _, backup := range completed {
		if backup.At.After(point) {
			break
		}
		base = backup
	}
	if base.At.Equal(point) || base.Binlog {
		return point, nil
	}
	return time.Time{}, &RestoreTargetError{fmt.Sprintf(
		"the binlogs after backup %s are missing, so %s cannot be reached; restore to a backup time instead (%s)",
		base.ID, point.Format(time.RFC3339), window)}
}

// expiredMySQLBackups are the runs retention removes: everything older than
// the newest run at or before cutoff, which stays so the window still
// reaches back to the cutoff. The newest run is never among them.
func expiredMySQLBackups(backups []mysqlBackup, cutoff time.Time) []mysqlBackup {
	keep := -1
	for i, backup := range backups {
		if backup.At.After(cutoff) {
			break
		}
		keep = i
	}
	if keep <= 0 {
		return nil
	}
	return append([]mysqlBackup(nil), backups[:keep]...)
}

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
	// Size is the bytes the run's directory holds.
	Size int64
	// Shipped are the binlogs the platform shipped after the run, by file
	// name, and ShipMark the key of how far they reach (EXC-587).
	Shipped  map[string]StoredObject
	ShipMark string
}

// readMySQLBackups groups the keys under prefix into runs, oldest first.
func readMySQLBackups(keys []string, prefix string) []mysqlBackup {
	objects := make([]StoredObject, 0, len(keys))
	for _, key := range keys {
		objects = append(objects, StoredObject{Key: key})
	}
	return readMySQLBackupObjects(objects, prefix)
}

// readMySQLBackupObjects groups the objects under prefix into runs, oldest
// first. Anything not in a run's directory is ignored, as MOCO's restore does.
func readMySQLBackupObjects(objects []StoredObject, prefix string) []mysqlBackup {
	runs := map[string]*mysqlBackup{}
	for _, object := range objects {
		key := object.Key
		rest := strings.TrimPrefix(key, prefix)
		if shipped, ok := strings.CutPrefix(rest, mysqlShippedDir+"/"); ok {
			addShippedObject(runs, shipped, object)
			continue
		}
		directory, file, ok := strings.Cut(rest, "/")
		if !ok || strings.Contains(file, "/") {
			continue
		}
		run := mysqlRunOf(runs, directory)
		if run == nil {
			continue
		}
		run.Keys = append(run.Keys, key)
		run.Size += object.Size
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

// mysqlRunOf is the run a directory names, nil when it names none.
func mysqlRunOf(runs map[string]*mysqlBackup, directory string) *mysqlBackup {
	when, err := time.Parse(k8s.MOCOBackupTimeFormat, directory)
	if err != nil {
		return nil
	}
	run, seen := runs[directory]
	if !seen {
		run = &mysqlBackup{ID: directory, At: when}
		runs[directory] = run
	}
	return run
}

// addShippedObject files "<run>/<file>" under its run: a shipped binlog or
// the run's ship mark. Its keys go with the run when retention removes it.
func addShippedObject(runs map[string]*mysqlBackup, shipped string, object StoredObject) {
	directory, file, ok := strings.Cut(shipped, "/")
	if !ok || strings.Contains(file, "/") {
		return
	}
	run := mysqlRunOf(runs, directory)
	if run == nil {
		return
	}
	run.Keys = append(run.Keys, object.Key)
	if file == mysqlShipMarkFile {
		run.ShipMark = object.Key
		return
	}
	name, gzipped := strings.CutSuffix(file, ".gz")
	if _, err := binlogIndex(name); err != nil || !gzipped {
		return
	}
	if run.Shipped == nil {
		run.Shipped = map[string]StoredObject{}
	}
	run.Shipped[name] = object
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

// mysqlRestoreWindow reaches from the first run to the newest run or, past
// it, to the last ship of that run's binlogs (EXC-587).
func mysqlRestoreWindow(backups []mysqlBackup, marks map[string]mysqlShipMark) (RestoreWindow, bool) {
	completed := completedMySQLBackups(backups)
	if len(completed) == 0 {
		return RestoreWindow{}, false
	}
	newest := completed[len(completed)-1]
	window := RestoreWindow{Earliest: completed[0].At, Latest: newest.At}
	if mark, ok := marks[newest.ID]; ok && mark.Through.After(window.Latest) {
		window.Latest = mark.Through
	}
	return window, true
}

// resolveMySQLRestorePoint is the time a restore asks for: a backup's own
// time, a time inside the window, or the newest backup when nothing is named.
func resolveMySQLRestorePoint(backups []mysqlBackup, marks map[string]mysqlShipMark, req domain.RestoreRequest) (time.Time, error) {
	if req.TargetXID != "" || req.TargetLSN != "" || req.TargetName != "" {
		return time.Time{}, &RestoreTargetError{"a MySQL project restores to a time only; transaction ids, LSNs and restore point names are Postgres targets"}
	}
	completed := completedMySQLBackups(backups)
	window, ok := mysqlRestoreWindow(completed, marks)
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
		return pointInWindow(completed, marks, window, req.TargetTime.Time)
	}
	return window.Latest, nil
}

// pointInWindow accepts a time MOCO can reach: a run's own time, a time
// after a run whose binlogs reach to the next one, or a time the binlogs
// shipped after the run reach. MOCO reads it to the second.
func pointInWindow(completed []mysqlBackup, marks map[string]mysqlShipMark, window RestoreWindow, wanted time.Time) (time.Time, error) {
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
	if mark, ok := marks[base.ID]; ok && !mark.Through.Before(point) {
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

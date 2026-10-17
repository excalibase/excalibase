package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A single host's MySQL backups (EXC-592, EXC-591) are written by the
// platform: each run is a directory "<time>-<server uuid>" holding a dump and,
// written last, its manifest; the closed binlogs of each server are kept once
// under binlogs/<server uuid>/. A run of server S reaches back to its dump and
// forward to the next run of S, whose flush closed the binlogs between them.
// A replacement server (an in-place restore) starts a lineage of its own: its
// binlogs never replay onto the other's dumps.

const (
	hostMySQLDumpFile     = "dump.sql.gz"
	hostMySQLManifestFile = "manifest.json"
	// hostMySQLServerIDLength is a server_uuid's length.
	hostMySQLServerIDLength = 36
)

// hostMySQLRun is one backup run as the bucket holds it.
type hostMySQLRun struct {
	ID       string
	At       time.Time
	Server   string
	Complete bool
	Keys     []string
	Size     int64
}

// hostMySQLManifest is what a run records beside its dump.
type hostMySQLManifest struct {
	Server string    `json:"serverUuid"`
	At     time.Time `json:"takenAt"`
	// FirstBinlog is the file the run's flush opened: transactions after the
	// dump are in it and later files, everything before it was shipped.
	FirstBinlog string `json:"firstBinlog"`
}

// hostMySQLRunDirectory is the run's directory under the runs prefix.
func hostMySQLRunDirectory(id, server string) string { return id + "-" + server }

// readHostMySQLRuns groups the objects under runsPrefix into runs, oldest
// first; anything that is not a run's directory is ignored.
func readHostMySQLRuns(objects []S3Object, runsPrefix string) []hostMySQLRun {
	runs := map[string]*hostMySQLRun{}
	for _, object := range objects {
		directory, file, ok := strings.Cut(strings.TrimPrefix(object.Key, runsPrefix), "/")
		if !ok || strings.Contains(file, "/") {
			continue
		}
		id, server, ok := splitHostMySQLRun(directory)
		if !ok {
			continue
		}
		when, err := time.Parse(k8s.MOCOBackupTimeFormat, id)
		if err != nil {
			continue
		}
		run, seen := runs[directory]
		if !seen {
			run = &hostMySQLRun{ID: id, At: when, Server: server}
			runs[directory] = run
		}
		run.Keys = append(run.Keys, object.Key)
		run.Size += object.SizeBytes
		run.Complete = run.Complete || file == hostMySQLManifestFile
	}
	all := make([]hostMySQLRun, 0, len(runs))
	for _, run := range runs {
		all = append(all, *run)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	return all
}

// splitHostMySQLRun reads "<time>-<server uuid>".
func splitHostMySQLRun(directory string) (string, string, bool) {
	cut := len(directory) - hostMySQLServerIDLength - 1
	if cut <= 0 || directory[cut] != '-' {
		return "", "", false
	}
	return directory[:cut], directory[cut+1:], true
}

func completedHostMySQLRuns(runs []hostMySQLRun) []hostMySQLRun {
	completed := make([]hostMySQLRun, 0, len(runs))
	for _, run := range runs {
		if run.Complete {
			completed = append(completed, run)
		}
	}
	return completed
}

// hostMySQLWindow reaches from the oldest run to the newest run or, past it,
// to the last ship of a server that has a run to replay onto (EXC-587).
func hostMySQLWindow(runs []hostMySQLRun, marks map[string]hostMySQLShipMark) (RestoreWindow, bool) {
	completed := completedHostMySQLRuns(runs)
	if len(completed) == 0 {
		return RestoreWindow{}, false
	}
	window := RestoreWindow{Earliest: completed[0].At, Latest: completed[len(completed)-1].At}
	for _, mark := range marks {
		if mark.Through.After(window.Latest) && hasHostMySQLRunBy(completed, mark.Server, mark.Through) {
			window.Latest = mark.Through
		}
	}
	return window, true
}

// hasHostMySQLRunBy reports whether server has a run at or before at.
func hasHostMySQLRunBy(completed []hostMySQLRun, server string, at time.Time) bool {
	for _, run := range completed {
		if run.Server == server && !run.At.After(at) {
			return true
		}
	}
	return false
}

// hostMySQLPoint is a resolved restore: the dump to load and, when Replay,
// the binlogs up to Cover's flush (or, when ShippedTo is set, up to that
// file the last ship opened) replayed until Point.
type hostMySQLPoint struct {
	Point     time.Time
	Base      hostMySQLRun
	Cover     hostMySQLRun
	Replay    bool
	ShippedTo string
}

// resolveHostMySQLPoint is the point a restore asks for: a backup's own time,
// a time a run and a later run of the same server reach, or the newest backup.
func resolveHostMySQLPoint(runs []hostMySQLRun, marks map[string]hostMySQLShipMark, req domain.RestoreRequest) (hostMySQLPoint, error) {
	if req.TargetXID != "" || req.TargetLSN != "" || req.TargetName != "" {
		return hostMySQLPoint{}, &RestoreTargetError{"a MySQL project restores to a time only; transaction ids, LSNs and restore point names are Postgres targets"}
	}
	completed := completedHostMySQLRuns(runs)
	window, ok := hostMySQLWindow(completed, marks)
	if !ok {
		return hostMySQLPoint{}, &RestoreTargetError{"this project has no completed backup to restore from yet"}
	}
	switch {
	case req.BackupID != "":
		for _, run := range completed {
			if run.ID == req.BackupID {
				return hostMySQLPoint{Point: run.At, Base: run, Cover: run}, nil
			}
		}
		return hostMySQLPoint{}, &RestoreTargetError{fmt.Sprintf("backup %s is not one of this project's completed backups", req.BackupID)}
	case req.TargetTime != nil:
		return hostMySQLPointInWindow(completed, marks, window, req.TargetTime.Time)
	}
	newest := completed[len(completed)-1]
	return hostMySQLPoint{Point: newest.At, Base: newest, Cover: newest}, nil
}

// hostMySQLPointInWindow reads the time to the second, as mysqlbinlog does.
func hostMySQLPointInWindow(completed []hostMySQLRun, marks map[string]hostMySQLShipMark, window RestoreWindow, wanted time.Time) (hostMySQLPoint, error) {
	point := wanted.UTC().Truncate(time.Second)
	if point.Before(window.Earliest) || point.After(window.Latest) {
		return hostMySQLPoint{}, &RestoreTargetError{fmt.Sprintf("%s is outside the %s", point.Format(time.RFC3339), window)}
	}
	var base hostMySQLRun
	for _, run := range completed {
		if run.At.After(point) {
			break
		}
		base = run
	}
	if base.At.Equal(point) {
		return hostMySQLPoint{Point: point, Base: base, Cover: base}, nil
	}
	for _, run := range completed {
		if run.Server == base.Server && !run.At.Before(point) {
			return hostMySQLPoint{Point: point, Base: base, Cover: run, Replay: true}, nil
		}
	}
	if mark, ok := marks[base.Server]; ok && !mark.Through.Before(point) {
		return hostMySQLPoint{Point: point, Base: base, Cover: base, Replay: true, ShippedTo: mark.NextBinlog}, nil
	}
	return hostMySQLPoint{}, &RestoreTargetError{fmt.Sprintf(
		"%s cannot be reached: the binlogs of the database that took backup %s are not shipped that far; restore to a backup time instead (%s)",
		point.Format(time.RFC3339), base.ID, window)}
}

// expiredHostMySQLRuns are the runs retention removes: everything older than
// the newest run at or before cutoff, which stays so the window still reaches
// back to the cutoff.
func expiredHostMySQLRuns(runs []hostMySQLRun, cutoff time.Time) []hostMySQLRun {
	keep := -1
	for i, run := range runs {
		if run.At.After(cutoff) {
			break
		}
		keep = i
	}
	if keep <= 0 {
		return nil
	}
	return append([]hostMySQLRun(nil), runs[:keep]...)
}

const binlogBase = "binlog."

// binlogIndex reads "binlog.000123" as 123.
func binlogIndex(file string) (int, error) {
	digits, ok := strings.CutPrefix(file, binlogBase)
	if !ok || digits == "" || strings.ContainsAny(digits, "/\\") {
		return 0, fmt.Errorf("%q is not a binlog file", file)
	}
	index, err := strconv.Atoi(digits)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("%q is not a binlog file", file)
	}
	return index, nil
}

// binlogName is the file name mysqld gives a binlog index.
func binlogName(index int) string { return fmt.Sprintf("%s%06d", binlogBase, index) }

// binlogRange lists the files from first up to, not including, end.
func binlogRange(first, end string) ([]string, error) {
	from, err := binlogIndex(first)
	if err != nil {
		return nil, err
	}
	to, err := binlogIndex(end)
	if err != nil {
		return nil, err
	}
	if to < from {
		return nil, fmt.Errorf("binlog range %s..%s runs backwards", first, end)
	}
	files := make([]string, 0, to-from)
	for index := from; index < to; index++ {
		files = append(files, binlogName(index))
	}
	return files, nil
}

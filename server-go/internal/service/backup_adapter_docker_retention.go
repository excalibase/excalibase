package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Retention of a single host's Postgres backups (EXC-593): the base backups
// a point inside the window needs, and the WAL they replay.

var _ RetentionPurger = (*DockerBackupAdapter)(nil)

// walSegment is a WAL segment's name: timeline, log, segment. Names order the
// way the server writes them.
var walSegment = regexp.MustCompile(`^[0-9A-F]{24}$`)

// startSegmentKey holds the segment a base backup starts in.
func (a *DockerBackupAdapter) startSegmentKey(projectID, backupID string) string {
	return fmt.Sprintf("%s%s/manual/%s.wal", a.keyPrefix, projectID, backupID)
}

// noteStartSegment records the segment the database writes as a backup
// begins: the backup replays nothing older. Without it retention keeps all
// of the project's WAL.
func (a *DockerBackupAdapter) noteStartSegment(ctx context.Context, inst *domain.DatabaseInstance, backupID string) {
	a.mu.RLock()
	dc := a.docker
	a.mu.RUnlock()
	if dc == nil {
		return
	}
	out, err := dc.ExecInContainerStdin(ctx, inst.Namespace,
		[]string{"psql", "-U", "postgres", "-tAc", "SELECT pg_walfile_name(pg_current_wal_lsn())"}, "")
	segment := strings.TrimSpace(out)
	if err != nil || !walSegment.MatchString(segment) {
		log.Printf("WARN: backup %s of %s: start segment unknown (%v); its WAL is kept", backupID, inst.ProjectID, err)
		return
	}
	if _, err := a.uploader.Upload(ctx, a.bucket, a.startSegmentKey(inst.ProjectID, backupID), strings.NewReader(segment)); err != nil {
		log.Printf("WARN: backup %s of %s: note its start segment: %v", backupID, inst.ProjectID, err)
	}
}

// datedBackup is a completed base backup and when it started.
type datedBackup struct {
	id string
	at time.Time
}

// PurgeExpired deletes the backups, and the WAL, no point inside the
// retention window needs. See RetentionPurger.
func (a *DockerBackupAdapter) PurgeExpired(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	if inst.BackupRetentionDays == nil || *inst.BackupRetentionDays <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrMySQLRetentionUnknown, inst.ProjectID)
	}
	// Only the current major's archive ages out; an earlier one, left
	// behind by a major upgrade, is kept until the project is deleted.
	records, err := a.majorRecords(ctx, inst)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-time.Duration(*inst.BackupRetentionDays) * 24 * time.Hour)
	expired, anchor := expiredBackups(records, cutoff)
	var doomed []string
	for _, backup := range expired {
		doomed = append(doomed, a.objectKey(inst.ProjectID, "manual", backup.id), a.startSegmentKey(inst.ProjectID, backup.id))
	}
	if anchor != nil {
		wal, err := a.unneededWAL(ctx, inst, anchor.id)
		if err != nil {
			return 0, err
		}
		doomed = append(doomed, wal...)
	}
	for count, key := range doomed {
		if err := a.uploader.Delete(ctx, a.bucket, key); err != nil {
			return count, fmt.Errorf("delete expired backup object %s: %w", key, err)
		}
	}
	if len(expired) > 0 {
		ids := make([]string, 0, len(expired))
		for _, backup := range expired {
			ids = append(ids, backup.id)
		}
		if err := a.records.DeleteRecords(ctx, inst.ProjectID, ids); err != nil {
			return len(doomed), fmt.Errorf("drop the records of expired backups: %w", err)
		}
	}
	return len(doomed), nil
}

// expiredBackups splits the completed backups at the last one started by
// cutoff (the anchor a point at the window's start recovers from): the ones
// before it are expired, oldest first.
func expiredBackups(records []domain.BackupRecord, cutoff time.Time) ([]datedBackup, *datedBackup) {
	var completed []datedBackup
	for _, record := range records {
		at, err := time.Parse(time.RFC3339, record.Timestamp)
		if err == nil && record.Status == "COMPLETED" {
			completed = append(completed, datedBackup{id: record.ID, at: at})
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].at.Before(completed[j].at) })
	anchor := -1
	for i, backup := range completed {
		if !backup.at.After(cutoff) {
			anchor = i
		}
	}
	if anchor < 0 {
		return nil, nil
	}
	return completed[:anchor], &completed[anchor]
}

// unneededWAL is the archived WAL older than the segment anchor starts in;
// histories stay. An anchor whose start is unknown keeps everything.
func (a *DockerBackupAdapter) unneededWAL(ctx context.Context, inst *domain.DatabaseInstance, anchorID string) ([]string, error) {
	body, err := a.uploader.Download(ctx, a.bucket, a.startSegmentKey(inst.ProjectID, anchorID))
	if err != nil {
		log.Printf("WARN: retention of %s: start of backup %s unknown; its WAL is kept", inst.ProjectID, anchorID)
		return nil, nil
	}
	start, err := readSmall(body)
	if err != nil || !walSegment.MatchString(start) {
		return nil, nil
	}
	prefix, err := a.walsPrefix(ctx, inst)
	if err != nil {
		return nil, err
	}
	objects, err := a.uploader.List(ctx, a.bucket, prefix)
	if err != nil {
		return nil, fmt.Errorf("list archived WAL: %w", err)
	}
	var doomed []string
	for _, object := range objects {
		name := strings.TrimSuffix(strings.TrimPrefix(object.Key, prefix), ".gz")
		if (walSegment.MatchString(name) || walBackupLabel(name)) && name[:24] < start {
			doomed = append(doomed, object.Key)
		}
	}
	return doomed, nil
}

// walBackupLabel is a backup history file, named for the segment it starts in.
func walBackupLabel(name string) bool {
	return walFileName.MatchString(name) && strings.HasSuffix(name, ".backup")
}

// readSmall reads a short note object and closes it.
func readSmall(body io.ReadCloser) (string, error) {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 256))
	return strings.TrimSpace(string(data)), err
}

package service

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// PurgeExpired deletes the runs retention no longer keeps and the binlogs no
// kept run needs, and returns how many objects went.
func (a *HostMySQLBackupAdapter) PurgeExpired(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	if inst.BackupRetentionDays == nil || *inst.BackupRetentionDays <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrMySQLRetentionUnknown, inst.ProjectID)
	}
	unlock := a.lock(inst.ProjectID)
	defer unlock()
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return 0, err
	}
	completed := completedHostMySQLRuns(runs)
	cutoff := a.now().Add(-time.Duration(*inst.BackupRetentionDays) * 24 * time.Hour)
	expired := expiredHostMySQLRuns(completed, cutoff)
	var doomed []string
	for _, run := range expired {
		doomed = append(doomed, run.Keys...)
	}
	binlogs, err := a.unneededBinlogs(ctx, inst.ProjectID, completed[len(expired):])
	if err != nil {
		return 0, err
	}
	doomed = append(doomed, binlogs...)
	for count, key := range doomed {
		if err := a.uploader.Delete(ctx, a.bucket, key); err != nil {
			return count, fmt.Errorf("delete expired backup object %s: %w", key, err)
		}
	}
	if len(expired) > 0 {
		if err := a.records.DeleteThrough(ctx, inst.ProjectID, expired[len(expired)-1].At); err != nil {
			return len(doomed), fmt.Errorf("drop the records of expired backups: %w", err)
		}
	}
	return len(doomed), nil
}

// unneededBinlogs are the binlogs older than the first one a kept run of
// their server replays from; a server with no kept run needs none, nor its
// ship mark.
func (a *HostMySQLBackupAdapter) unneededBinlogs(ctx context.Context, projectID string, kept []hostMySQLRun) ([]string, error) {
	oldest := map[string]int{}
	for _, run := range kept {
		manifest, err := a.manifestOf(ctx, projectID, run)
		if err != nil {
			return nil, err
		}
		index, _ := binlogIndex(manifest.FirstBinlog)
		if current, seen := oldest[run.Server]; !seen || index < current {
			oldest[run.Server] = index
		}
	}
	prefix := a.projectPrefix(projectID) + "binlogs/"
	objects, err := a.uploader.List(ctx, a.bucket, prefix)
	if err != nil {
		return nil, fmt.Errorf("list shipped binlogs: %w", err)
	}
	var unneeded []string
	for _, object := range objects {
		server, file, ok := strings.Cut(strings.TrimPrefix(object.Key, prefix), "/")
		if _, needed := oldest[server]; ok && !needed && file == hostMySQLShipMarkFile {
			unneeded = append(unneeded, object.Key)
			continue
		}
		index, err := binlogIndex(strings.TrimSuffix(path.Base(file), ".gz"))
		if !ok || err != nil {
			continue
		}
		if first, needed := oldest[server]; !needed || index < first {
			unneeded = append(unneeded, object.Key)
		}
	}
	return unneeded, nil
}

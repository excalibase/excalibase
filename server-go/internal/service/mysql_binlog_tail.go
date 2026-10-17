package service

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A restore to a point past the newest run MOCO can reach by itself is
// MOCO's restore of that run, then the binlogs shipped after it replayed on
// the new primary up to the point (EXC-587). GTIDs skip what the dump holds.

const mysqlReplayDir = "/tmp/excalibase-replay"

// mysqlTail is the shipped binlogs a restore replays after MOCO's.
type mysqlTail struct {
	files  []StoredObject
	stopAt time.Time
}

// catalog is the project's runs and, for every run whose binlogs MOCO has
// not written, how far the binlogs shipped after it reach.
func (a *MySQLBackupAdapter) catalog(ctx context.Context, inst *domain.DatabaseInstance) ([]mysqlBackup, map[string]mysqlShipMark, error) {
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return nil, nil, err
	}
	marks := map[string]mysqlShipMark{}
	var store *domain.S3Credentials
	var objects mysqlObjectStore
	for _, backup := range completedMySQLBackups(backups) {
		if backup.Binlog || backup.ShipMark == "" {
			continue
		}
		if objects == nil {
			if store, objects, err = a.openReadWriter(ctx); err != nil {
				return nil, nil, err
			}
		}
		mark, err := readMySQLShipMark(ctx, objects, store.Bucket, backup.ShipMark)
		if err != nil {
			return nil, nil, err
		}
		if mark.Run != backup.ID {
			return nil, nil, fmt.Errorf("ship mark %s names run %s", backup.ShipMark, mark.Run)
		}
		marks[backup.ID] = mark
	}
	return backups, marks, nil
}

// mysqlRestoreAt is where MOCO restores to for point and the tail replayed
// after it, nil when MOCO reaches the point itself.
func mysqlRestoreAt(backups []mysqlBackup, marks map[string]mysqlShipMark, point time.Time) (time.Time, *mysqlTail, error) {
	var base mysqlBackup
	for _, backup := range completedMySQLBackups(backups) {
		if backup.At.After(point) {
			break
		}
		base = backup
	}
	if base.ID == "" {
		return time.Time{}, nil, &RestoreTargetError{fmt.Sprintf("no backup was taken by %s", point.UTC().Format(time.RFC3339))}
	}
	if base.At.Equal(point) || base.Binlog {
		return point, nil, nil
	}
	mark, ok := marks[base.ID]
	if !ok || mark.Through.Before(point) {
		return time.Time{}, nil, &RestoreTargetError{fmt.Sprintf("the binlogs after backup %s do not reach %s", base.ID, point.UTC().Format(time.RFC3339))}
	}
	names, err := binlogRange(mark.FirstBinlog, mark.NextBinlog)
	if err != nil {
		return time.Time{}, nil, err
	}
	tail := &mysqlTail{stopAt: point}
	for _, name := range names {
		object, ok := base.Shipped[name]
		if !ok {
			return time.Time{}, nil, &RestoreTargetError{fmt.Sprintf("shipped binlog %s after backup %s is missing, so %s cannot be reached; restore to a backup time instead",
				name, base.ID, point.UTC().Format(time.RFC3339))}
		}
		tail.files = append(tail.files, object)
	}
	return base.At, tail, nil
}

// replayTail replays the tail on the project's primary through MOCO's admin.
func (a *MySQLBackupAdapter) replayTail(ctx context.Context, namespace, projectID string, tail *mysqlTail) error {
	if tail == nil {
		return nil
	}
	if a.streamer == nil {
		return errors.New("replaying shipped binlogs needs pod exec, which is not wired")
	}
	cluster, err := a.kube.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return fmt.Errorf("read the restored cluster: %w", err)
	}
	store, objects, err := a.openReadWriter(ctx)
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(writeMySQLTailArchive(ctx, objects, store.Bucket, tail.files, writer)) }()
	cmd := []string{"bash", "-c", mysqlReplayScript(tail)}
	err = a.streamer.StreamExecInPod(ctx, namespace, k8s.MySQLPrimaryPod(cluster), provisioner.MOCOMySQLContainer, cmd, reader, io.Discard)
	_ = reader.CloseWithError(err)
	if err != nil {
		return fmt.Errorf("replay the shipped binlogs to %s: %w", tail.stopAt.UTC().Format(time.RFC3339), err)
	}
	return nil
}

// writeMySQLTailArchive streams the shipped files from the bucket as a tar.
func writeMySQLTailArchive(ctx context.Context, objects objectReadWriter, bucket string, files []StoredObject, writer io.Writer) error {
	archive := tar.NewWriter(writer)
	for _, object := range files {
		if object.Size <= 0 {
			return fmt.Errorf("the store reports no size for %s", object.Key)
		}
		body, err := objects.Download(ctx, bucket, object.Key)
		if err != nil {
			return fmt.Errorf("download %s: %w", object.Key, err)
		}
		name := object.Key[strings.LastIndex(object.Key, "/")+1:]
		err = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: object.Size, ModTime: time.Now()})
		if err == nil {
			_, err = io.CopyN(archive, body, object.Size)
		}
		_ = body.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", object.Key, err)
		}
	}
	return archive.Close()
}

// mysqlReplayScript unpacks the archive on stdin and replays it to the point
// as MOCO's own restore replays binlogs: mysqlbinlog in UTC into mysql. File
// names were checked as binlog names when the catalog was read.
func mysqlReplayScript(tail *mysqlTail) string {
	files := make([]string, 0, len(tail.files))
	for _, object := range tail.files {
		name := strings.TrimSuffix(object.Key[strings.LastIndex(object.Key, "/")+1:], ".gz")
		files = append(files, mysqlReplayDir+"/"+name)
	}
	return `set -euo pipefail
dir=` + mysqlReplayDir + `
rm -rf "$dir"
mkdir -p "$dir"
trap 'rm -rf "$dir"' EXIT
tar -x -C "$dir"
gunzip "$dir"/` + binlogBase + `*.gz
TZ=Etc/UTC mysqlbinlog --stop-datetime='` + tail.stopAt.UTC().Format(mysqlbinlogTimeFormat) + `' ` + strings.Join(files, " ") +
		` | mysql --defaults-extra-file=` + provisioner.MOCOAdminDefaultsFile + ` --binary-mode
`
}

// MySQLBinlogSweep ships Kubernetes MySQL projects' binlogs between backups
// (EXC-587). Only the replica that leads ships, so two never flush at once.
type MySQLBinlogSweep struct {
	adapter   *MySQLBackupAdapter
	instances storage.InstanceStore
	leader    *Leadership
}

func NewMySQLBinlogSweep(adapter *MySQLBackupAdapter, instances storage.InstanceStore, leader *Leadership) *MySQLBinlogSweep {
	return &MySQLBinlogSweep{adapter: adapter, instances: instances, leader: leader}
}

// Run sweeps every interval until ctx ends.
func (s *MySQLBinlogSweep) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep is one pass; a project's failure is logged and the next goes on.
func (s *MySQLBinlogSweep) Sweep(ctx context.Context) {
	leading, err := s.leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: mysql binlog sweep: leadership: %v", err)
		return
	}
	if !leading {
		return
	}
	projects, err := s.instances.FindAll()
	if err != nil {
		log.Printf("ERROR: mysql binlog sweep: list projects: %v", err)
		return
	}
	for _, inst := range projects {
		if inst.DBType != domain.MySQL || inst.DeploymentMode != domain.ModeK8s || inst.NoDatabase ||
			!domain.IsActive(inst.Status) || !backupsOn(inst) {
			continue
		}
		if err := s.adapter.ShipBinlogs(ctx, inst); err != nil {
			log.Printf("ERROR: mysql binlog sweep %s: %v", inst.ProjectID, err)
		}
	}
}

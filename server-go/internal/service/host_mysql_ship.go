package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A run: flush the binlogs (closing every file written so far), dump the
// databases and accounts, ship the closed binlogs not yet shipped, and write
// the manifest last, which is what makes the run complete.

const (
	// hostMySQLDumpScript dumps every project database with the GTIDs it
	// holds, then the accounts with their password hashes and grants, so a
	// restore brings users back as MOCO's dump does. Account names reach the
	// shell only as SQL text built by QUOTE(), never as shell words.
	hostMySQLDumpScript = `set -euo pipefail
client=(mysql --socket=` + provisioner.MySQLSocket + ` -uroot --batch --skip-column-names --raw)
mapfile -t databases < <("${client[@]}" -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','sys','information_schema','performance_schema') ORDER BY schema_name")
{
  mysqldump --socket=` + provisioner.MySQLSocket + ` -uroot --single-transaction --set-gtid-purged=ON --routines --events --triggers --hex-blob --max-allowed-packet=1073741824 --databases "${databases[@]}"
  { echo "SET SESSION print_identified_with_as_hex=ON;"
    "${client[@]}" -e "SELECT CONCAT('SHOW CREATE USER ', QUOTE(user), '@', QUOTE(host), '; SHOW GRANTS FOR ', QUOTE(user), '@', QUOTE(host), ';') FROM mysql.user WHERE user <> 'root' AND user NOT LIKE 'mysql.%' ORDER BY user, host"
  } | "${client[@]}" | sed 's/$/;/'
} | gzip -1`
	binlogDir = provisioner.MySQLDataDir + "/"
)

// hostMySQLServerState is what a flush leaves: the server and the binlog
// file it opened.
type hostMySQLServerState struct {
	Server      string
	FirstBinlog string
}

func (a *HostMySQLBackupAdapter) backup(ctx context.Context, inst *domain.DatabaseInstance, kind string) (BackupRef, error) {
	if !a.BackupsConfigured() {
		return BackupRef{}, ErrBackupStorageNotConfigured
	}
	unlock := a.lock(inst.ProjectID)
	defer unlock()
	if status, err := a.docker.ContainerStatus(ctx, inst.Namespace); err != nil || status != "running" {
		return BackupRef{}, errors.Join(ErrHostMySQLNotRunning, err)
	}
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return BackupRef{}, err
	}
	run := hostMySQLRun{ID: a.nextRunID(runs)}
	started := a.now().UTC()
	record := &domain.BackupRecord{ID: run.ID, ProjectID: inst.ProjectID, Timestamp: started.Format(time.RFC3339), Type: kind, Status: backupStatusInProgress}
	if err := a.records.Save(ctx, record); err != nil {
		return BackupRef{}, fmt.Errorf("record the backup: %w", err)
	}
	size, err := a.ship(ctx, inst, &run, started)
	if err != nil {
		if statusErr := a.records.UpdateStatus(ctx, run.ID, backupStatusFailed); statusErr != nil {
			log.Printf("WARN: record failed backup %s of %s: %v", run.ID, inst.ProjectID, statusErr)
		}
		return BackupRef{}, fmt.Errorf("back up %s: %w", inst.ProjectID, err)
	}
	if err := a.records.UpdateStatus(ctx, run.ID, backupStatusCompleted); err != nil {
		log.Printf("WARN: record completed backup %s of %s: %v", run.ID, inst.ProjectID, err)
	}
	return BackupRef{ID: run.ID, ProjectID: inst.ProjectID, Type: kind, Status: backupStatusCompleted,
		StartedAt: started.Format(time.RFC3339), FinishedAt: a.now().UTC().Format(time.RFC3339), SizeBytes: size}, nil
}

// ship writes the run; a run that fails leaves no dump behind.
func (a *HostMySQLBackupAdapter) ship(ctx context.Context, inst *domain.DatabaseInstance, run *hostMySQLRun, started time.Time) (int64, error) {
	state, err := a.flush(ctx, inst.Namespace)
	if err != nil {
		return 0, err
	}
	run.Server = state.Server
	dumpKey := a.runKey(inst.ProjectID, *run, hostMySQLDumpFile)
	size, err := a.dumpTo(ctx, inst.Namespace, dumpKey)
	if err != nil {
		if deleteErr := a.uploader.Delete(ctx, a.bucket, dumpKey); deleteErr != nil {
			log.Printf("WARN: remove partial dump %s: %v", dumpKey, deleteErr)
		}
		return 0, err
	}
	shipped, err := a.shipBinlogs(ctx, inst, state)
	if err != nil {
		return 0, err
	}
	manifest, err := json.Marshal(hostMySQLManifest{Server: state.Server, At: started, FirstBinlog: state.FirstBinlog})
	if err != nil {
		return 0, err
	}
	if _, err := a.uploader.Upload(ctx, a.bucket, a.runKey(inst.ProjectID, *run, hostMySQLManifestFile), bytes.NewReader(manifest)); err != nil {
		return 0, fmt.Errorf("write the run's manifest: %w", err)
	}
	return size + shipped, nil
}

// flush closes the binlog in use and reads the server and the file it opened.
func (a *HostMySQLBackupAdapter) flush(ctx context.Context, container string) (hostMySQLServerState, error) {
	out, err := a.docker.ExecInContainerStdin(ctx, container, mysqlRootBatch(),
		"FLUSH BINARY LOGS;\nSELECT @@server_uuid, JSON_UNQUOTE(JSON_EXTRACT(LOCAL, '$.binary_log_file')) FROM performance_schema.log_status;\n")
	if err != nil {
		return hostMySQLServerState{}, fmt.Errorf("flush the binlogs: %w", err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || len(fields[0]) != hostMySQLServerIDLength {
		return hostMySQLServerState{}, fmt.Errorf("unexpected binlog state %q", strings.TrimSpace(out))
	}
	if _, err := binlogIndex(fields[1]); err != nil {
		return hostMySQLServerState{}, err
	}
	return hostMySQLServerState{Server: fields[0], FirstBinlog: fields[1]}, nil
}

// serverID is the running server's uuid.
func (a *HostMySQLBackupAdapter) serverID(ctx context.Context, container string) (string, error) {
	out, err := a.docker.ExecInContainerStdin(ctx, container, mysqlRootBatch(), "SELECT @@server_uuid;\n")
	if err != nil {
		return "", fmt.Errorf("read the server id: %w", err)
	}
	server := strings.TrimSpace(out)
	if len(server) != hostMySQLServerIDLength {
		return "", fmt.Errorf("unexpected server id %q", server)
	}
	return server, nil
}

func mysqlRootBatch() []string {
	return []string{"mysql", "--socket=" + provisioner.MySQLSocket, "-uroot", "--batch", "--skip-column-names"}
}

// dumpTo streams the dump out of the container into key.
func (a *HostMySQLBackupAdapter) dumpTo(ctx context.Context, container, key string) (int64, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := a.streamer.StreamExec(ctx, container, []string{"bash", "-c", hostMySQLDumpScript}, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	size, uploadErr := a.uploader.Upload(ctx, a.bucket, key, reader)
	_ = reader.CloseWithError(uploadErr)
	if dumpErr := <-done; dumpErr != nil {
		return 0, fmt.Errorf("dump: %w", dumpErr)
	}
	if uploadErr != nil {
		return 0, fmt.Errorf("upload the dump: %w", uploadErr)
	}
	return size, nil
}

// shipBinlogs uploads, gzipped, every binlog the flush closed that is not in
// the bucket yet, and returns the bytes written.
func (a *HostMySQLBackupAdapter) shipBinlogs(ctx context.Context, inst *domain.DatabaseInstance, state hostMySQLServerState) (int64, error) {
	closed, err := a.closedBinlogs(ctx, inst.Namespace, state.FirstBinlog)
	if err != nil {
		return 0, err
	}
	prefix := a.binlogsPrefix(inst.ProjectID, state.Server)
	present, err := a.uploader.List(ctx, a.bucket, prefix)
	if err != nil {
		return 0, fmt.Errorf("list shipped binlogs: %w", err)
	}
	shipped := map[string]bool{}
	for _, object := range present {
		shipped[strings.TrimSuffix(path.Base(object.Key), ".gz")] = true
	}
	var total int64
	for _, file := range closed {
		if shipped[file] {
			continue
		}
		size, err := a.shipBinlog(ctx, inst.Namespace, file, prefix+file+".gz")
		if err != nil {
			return total, err
		}
		total += size
	}
	return total, nil
}

// closedBinlogs are the server's binlogs before the one the flush opened.
func (a *HostMySQLBackupAdapter) closedBinlogs(ctx context.Context, container, first string) ([]string, error) {
	out, err := a.docker.ExecInContainerStdin(ctx, container, mysqlRootBatch(), "SHOW BINARY LOGS;\n")
	if err != nil {
		return nil, fmt.Errorf("list the binlogs: %w", err)
	}
	end, err := binlogIndex(first)
	if err != nil {
		return nil, err
	}
	var closed []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		file, _, _ := strings.Cut(line, "\t")
		if index, err := binlogIndex(file); err == nil && index < end {
			closed = append(closed, file)
		}
	}
	return closed, nil
}

// shipBinlog copies one binlog out of the container and uploads it gzipped.
func (a *HostMySQLBackupAdapter) shipBinlog(ctx context.Context, container, file, key string) (int64, error) {
	archive, err := a.docker.CopyFromContainer(ctx, container, binlogDir+file)
	if err != nil {
		return 0, fmt.Errorf("copy %s out: %w", file, err)
	}
	defer archive.Close()
	entries := tar.NewReader(archive)
	if _, err := entries.Next(); err != nil {
		return 0, fmt.Errorf("read %s: %w", file, err)
	}
	reader, writer := io.Pipe()
	go func() {
		compressed := gzip.NewWriter(writer)
		_, err := io.Copy(compressed, entries)
		writer.CloseWithError(errors.Join(err, compressed.Close()))
	}()
	size, err := a.uploader.Upload(ctx, a.bucket, key, reader)
	_ = reader.CloseWithError(err)
	if err != nil {
		return 0, fmt.Errorf("upload %s: %w", file, err)
	}
	return size, nil
}

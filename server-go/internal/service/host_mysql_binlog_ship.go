package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Between backups the platform ships each server's binlogs as they close
// (EXC-587), so a point-in-time restore reaches the last ship and not only
// the last backup. A ship reads the time and the committed GTIDs, flushes
// (only when something was committed since the last ship), uploads the
// closed files, and records how far they reach in the server's ship mark.
// Every transaction committed before that time is in a shipped file.

const (
	hostMySQLShipMarkFile = "shipped.json"
	// hostMySQLCommitStateSQL reads the clock and what is committed; the GTID
	// set is printed on one line so batch output keeps it in one field.
	hostMySQLCommitStateSQL = "SELECT @@server_uuid, DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%dT%H:%i:%sZ'), " +
		"REPLACE(@@global.gtid_executed, '\\n', '');\n"
)

// hostMySQLShipMark is how far a server's shipped binlogs reach: every
// transaction committed before Through is in a file before NextBinlog.
type hostMySQLShipMark struct {
	Server     string    `json:"serverUuid"`
	Through    time.Time `json:"through"`
	NextBinlog string    `json:"nextBinlog"`
	GTIDs      string    `json:"gtidExecuted"`
}

// hostMySQLCommitState is the server, its clock and its committed GTIDs.
type hostMySQLCommitState struct {
	Server string
	At     time.Time
	GTIDs  string
}

// ShipBinlogs ships the project's newly closed binlogs. A stopped project, or
// a server with no backup to replay onto yet, is skipped.
func (a *HostMySQLBackupAdapter) ShipBinlogs(ctx context.Context, inst *domain.DatabaseInstance) error {
	if !a.BackupsConfigured() {
		return ErrBackupStorageNotConfigured
	}
	unlock := a.lock(inst.ProjectID)
	defer unlock()
	status, err := a.docker.ContainerStatus(ctx, inst.Namespace)
	if err != nil {
		return fmt.Errorf("read the state of %s: %w", inst.Namespace, err)
	}
	if status != "running" {
		return nil
	}
	state, err := a.commitState(ctx, inst.Namespace)
	if err != nil {
		return err
	}
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if !hasHostMySQLRunBy(completedHostMySQLRuns(runs), state.Server, state.At) {
		return nil
	}
	marks, err := a.shipMarks(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	mark, shipped := marks[state.Server]
	if shipped && mark.GTIDs == state.GTIDs {
		mark.Through = state.At
		return a.writeShipMark(ctx, inst.ProjectID, mark)
	}
	flushed, err := a.flush(ctx, inst.Namespace)
	if err != nil {
		return err
	}
	if flushed.Server != state.Server {
		return fmt.Errorf("the server of %s changed during the ship", inst.ProjectID)
	}
	if _, err := a.shipBinlogs(ctx, inst, flushed); err != nil {
		return err
	}
	return a.writeShipMark(ctx, inst.ProjectID, hostMySQLShipMark{
		Server: state.Server, Through: state.At, NextBinlog: flushed.FirstBinlog, GTIDs: state.GTIDs,
	})
}

// commitState reads the server, its UTC clock and its committed GTIDs at once.
func (a *HostMySQLBackupAdapter) commitState(ctx context.Context, container string) (hostMySQLCommitState, error) {
	out, err := a.docker.ExecInContainerStdin(ctx, container, mysqlRootBatch(), hostMySQLCommitStateSQL)
	if err != nil {
		return hostMySQLCommitState{}, fmt.Errorf("read the committed transactions: %w", err)
	}
	return parseMySQLCommitState(out)
}

// parseMySQLCommitState reads "<uuid>\t<time>\t<gtid set>"; a server that
// committed nothing has an empty set.
func parseMySQLCommitState(out string) (hostMySQLCommitState, error) {
	fields := strings.Split(strings.TrimRight(out, "\r\n"), "\t")
	if len(fields) != 3 || len(fields[0]) != hostMySQLServerIDLength {
		return hostMySQLCommitState{}, fmt.Errorf("unexpected commit state %q", strings.TrimSpace(out))
	}
	at, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return hostMySQLCommitState{}, fmt.Errorf("unexpected server time %q", fields[1])
	}
	return hostMySQLCommitState{Server: fields[0], At: at.UTC(), GTIDs: fields[2]}, nil
}

func (a *HostMySQLBackupAdapter) shipMarkKey(projectID, server string) string {
	return a.binlogsPrefix(projectID, server) + hostMySQLShipMarkFile
}

// shipMarks reads every server's ship mark, keyed by server.
func (a *HostMySQLBackupAdapter) shipMarks(ctx context.Context, projectID string) (map[string]hostMySQLShipMark, error) {
	objects, err := a.uploader.List(ctx, a.bucket, a.projectPrefix(projectID)+"binlogs/")
	if err != nil {
		return nil, fmt.Errorf("list the ship marks of %s: %w", projectID, err)
	}
	marks := map[string]hostMySQLShipMark{}
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, "/"+hostMySQLShipMarkFile) {
			continue
		}
		mark, err := a.readShipMark(ctx, object.Key)
		if err != nil {
			return nil, err
		}
		if object.Key != a.shipMarkKey(projectID, mark.Server) {
			return nil, fmt.Errorf("ship mark %s names server %s", object.Key, mark.Server)
		}
		marks[mark.Server] = mark
	}
	return marks, nil
}

func (a *HostMySQLBackupAdapter) readShipMark(ctx context.Context, key string) (hostMySQLShipMark, error) {
	body, err := a.uploader.Download(ctx, a.bucket, key)
	if err != nil {
		return hostMySQLShipMark{}, fmt.Errorf("read ship mark %s: %w", key, err)
	}
	defer body.Close()
	return decodeShipMark(body, key)
}

func decodeShipMark(body io.Reader, key string) (hostMySQLShipMark, error) {
	var mark hostMySQLShipMark
	if err := json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&mark); err != nil {
		return hostMySQLShipMark{}, fmt.Errorf("read ship mark %s: %w", key, err)
	}
	if _, err := binlogIndex(mark.NextBinlog); err != nil || mark.Through.IsZero() {
		return hostMySQLShipMark{}, fmt.Errorf("ship mark %s is incomplete", key)
	}
	return mark, nil
}

func (a *HostMySQLBackupAdapter) writeShipMark(ctx context.Context, projectID string, mark hostMySQLShipMark) error {
	body, err := json.Marshal(mark)
	if err != nil {
		return err
	}
	if _, err := a.uploader.Upload(ctx, a.bucket, a.shipMarkKey(projectID, mark.Server), bytes.NewReader(body)); err != nil {
		return fmt.Errorf("write the ship mark of %s: %w", projectID, err)
	}
	return nil
}

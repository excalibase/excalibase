package service

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A restore loads a run's dump into a fresh container of the same version
// and, for a point after the run, replays the same server's binlogs up to it
// (mysqlbinlog --stop-datetime, to the second). GTIDs skip what the dump
// already holds.

const (
	hostMySQLRestoreDir = "excalibase-restore"
	hostMySQLRestoreAt  = "/tmp"
	// mysqlbinlogTimeFormat is --stop-datetime's; the container runs in UTC.
	mysqlbinlogTimeFormat = "2006-01-02 15:04:05"
)

// hostMySQLLoad is what a restore puts into a container.
type hostMySQLLoad struct {
	dumpKey  string
	dumpSize int64
	binlogs  []S3Object
	stopAt   string
}

// planLoad lists the objects a point needs and refuses a point whose binlogs
// are not all in the bucket.
func (a *HostMySQLBackupAdapter) planLoad(ctx context.Context, projectID string, point hostMySQLPoint) (hostMySQLLoad, error) {
	load := hostMySQLLoad{dumpKey: a.runKey(projectID, point.Base, hostMySQLDumpFile)}
	runObjects, err := a.uploader.List(ctx, a.bucket, a.runKey(projectID, point.Base, ""))
	if err != nil {
		return hostMySQLLoad{}, fmt.Errorf("list backup %s: %w", point.Base.ID, err)
	}
	found := false
	for _, object := range runObjects {
		if object.Key == load.dumpKey {
			load.dumpSize, found = object.SizeBytes, true
		}
	}
	if !found {
		return hostMySQLLoad{}, fmt.Errorf("backup %s has no dump in the bucket", point.Base.ID)
	}
	if !point.Replay {
		return load, nil
	}
	first, err := a.manifestOf(ctx, projectID, point.Base)
	if err != nil {
		return hostMySQLLoad{}, err
	}
	end, err := a.manifestOf(ctx, projectID, point.Cover)
	if err != nil {
		return hostMySQLLoad{}, err
	}
	files, err := binlogRange(first.FirstBinlog, end.FirstBinlog)
	if err != nil {
		return hostMySQLLoad{}, err
	}
	shipped, err := a.uploader.List(ctx, a.bucket, a.binlogsPrefix(projectID, point.Base.Server))
	if err != nil {
		return hostMySQLLoad{}, fmt.Errorf("list shipped binlogs: %w", err)
	}
	byName := map[string]S3Object{}
	for _, object := range shipped {
		byName[strings.TrimSuffix(path.Base(object.Key), ".gz")] = object
	}
	for _, file := range files {
		object, ok := byName[file]
		if !ok {
			return hostMySQLLoad{}, &RestoreTargetError{fmt.Sprintf("binlog %s after backup %s is missing, so %s cannot be reached; restore to a backup time instead",
				file, point.Base.ID, point.Point.Format(time.RFC3339))}
		}
		load.binlogs = append(load.binlogs, object)
	}
	load.stopAt = point.Point.UTC().Format(mysqlbinlogTimeFormat)
	return load, nil
}

func (a *HostMySQLBackupAdapter) manifestOf(ctx context.Context, projectID string, run hostMySQLRun) (hostMySQLManifest, error) {
	body, err := a.uploader.Download(ctx, a.bucket, a.runKey(projectID, run, hostMySQLManifestFile))
	if err != nil {
		return hostMySQLManifest{}, fmt.Errorf("read the manifest of backup %s: %w", run.ID, err)
	}
	defer body.Close()
	var manifest hostMySQLManifest
	if err := json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&manifest); err != nil {
		return hostMySQLManifest{}, fmt.Errorf("read the manifest of backup %s: %w", run.ID, err)
	}
	if manifest.Server != run.Server {
		return hostMySQLManifest{}, fmt.Errorf("the manifest of backup %s names server %s", run.ID, manifest.Server)
	}
	if _, err := binlogIndex(manifest.FirstBinlog); err != nil {
		return hostMySQLManifest{}, fmt.Errorf("the manifest of backup %s: %w", run.ID, err)
	}
	return manifest, nil
}

// loadInto copies the dump and binlogs into the running, empty container and
// replays them.
func (a *HostMySQLBackupAdapter) loadInto(ctx context.Context, container string, load hostMySQLLoad) error {
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(a.writeLoadArchive(ctx, writer, load)) }()
	if err := a.docker.CopyToContainer(ctx, container, hostMySQLRestoreAt, reader); err != nil {
		_ = reader.CloseWithError(err)
		return fmt.Errorf("copy the backup in: %w", err)
	}
	if err := a.streamer.StreamExec(ctx, container, []string{"bash", "-c", hostMySQLLoadScript(load)}, io.Discard); err != nil {
		return fmt.Errorf("load the backup: %w", err)
	}
	return nil
}

// writeLoadArchive streams the run's objects from the bucket as a tar owned
// by the image's user.
func (a *HostMySQLBackupAdapter) writeLoadArchive(ctx context.Context, writer io.Writer, load hostMySQLLoad) error {
	archive := tar.NewWriter(writer)
	owner := provisioner.MySQLImageOwner
	if err := archive.WriteHeader(&tar.Header{Name: hostMySQLRestoreDir + "/", Typeflag: tar.TypeDir, Mode: 0o700,
		Uid: owner.UID, Gid: owner.GID, ModTime: a.now()}); err != nil {
		return err
	}
	objects := append([]S3Object{{Key: load.dumpKey, SizeBytes: load.dumpSize}}, load.binlogs...)
	for _, object := range objects {
		if err := a.addToArchive(ctx, archive, object); err != nil {
			return err
		}
	}
	return archive.Close()
}

func (a *HostMySQLBackupAdapter) addToArchive(ctx context.Context, archive *tar.Writer, object S3Object) error {
	body, err := a.uploader.Download(ctx, a.bucket, object.Key)
	if err != nil {
		return fmt.Errorf("download %s: %w", object.Key, err)
	}
	defer body.Close()
	owner := provisioner.MySQLImageOwner
	header := &tar.Header{Name: hostMySQLRestoreDir + "/" + path.Base(object.Key), Mode: 0o600, Size: object.SizeBytes,
		Uid: owner.UID, Gid: owner.GID, ModTime: a.now()}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.CopyN(archive, body, object.SizeBytes); err != nil {
		return fmt.Errorf("copy %s: %w", object.Key, err)
	}
	return nil
}

// hostMySQLLoadScript loads the dump into a server whose GTID history is
// emptied first (GTID_PURGED needs that), replays the binlogs to the point,
// and removes the copies.
func hostMySQLLoadScript(load hostMySQLLoad) string {
	dir := hostMySQLRestoreAt + "/" + hostMySQLRestoreDir
	script := `set -euo pipefail
client=(mysql --socket=` + provisioner.MySQLSocket + ` -uroot --max-allowed-packet=1073741824)
"${client[@]}" -e "RESET BINARY LOGS AND GTIDS"
gunzip -c ` + dir + `/` + hostMySQLDumpFile + ` | "${client[@]}"
`
	if len(load.binlogs) > 0 {
		files := make([]string, 0, len(load.binlogs))
		for _, object := range load.binlogs {
			files = append(files, dir+"/"+strings.TrimSuffix(path.Base(object.Key), ".gz"))
		}
		script += `gunzip ` + dir + `/` + binlogBase + `*.gz
mysqlbinlog --stop-datetime='` + load.stopAt + `' ` + strings.Join(files, " ") + ` | "${client[@]}"
`
	}
	return script + `rm -rf ` + dir + "\n"
}

// hostMySQLImage is the version's catalogue image; a restore never changes it.
func hostMySQLImage(inst *domain.DatabaseInstance) (string, error) {
	return config.MySQLImage(inst.MySQLVersion)
}

// plannedRestore is a resolved restore: what to load and where it runs.
type plannedRestore struct {
	load  hostMySQLLoad
	image string
	plan  RestorePlan
}

func (a *HostMySQLBackupAdapter) planRestore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (plannedRestore, error) {
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return plannedRestore{}, err
	}
	point, err := resolveHostMySQLPoint(runs, req)
	if err != nil {
		return plannedRestore{}, err
	}
	return a.planRestoreAt(ctx, inst, point)
}

// build starts an empty container for projectID and loads the backup into it.
func (a *HostMySQLBackupAdapter) build(ctx context.Context, pc *provisioner.ProvisionContext, projectID string, restore plannedRestore) (string, error) {
	container, err := a.mysql.StartEmpty(ctx, projectID, restore.image, restore.plan.Config)
	if container != "" {
		pc.RegisterCleanup("remove the restored container", func(ctx context.Context) error {
			return a.mysql.RemoveContainer(ctx, container)
		})
	}
	if err != nil {
		return "", err
	}
	if err := a.loadInto(ctx, container, restore.load); err != nil {
		return "", err
	}
	log.Printf("mysql restore: %s loaded from %s", container, restore.load.dumpKey)
	return container, nil
}

package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// walArchivePath is where archive_command drops closed WAL segments inside
// the project's container (provisioner.EnableWALArchive).
const walArchivePath = provisioner.WALArchiveDir

const pgDataPath = "/var/lib/postgresql/data"

// walFileName matches what archive_command leaves: a segment, a timeline
// history, or a backup history file. Anything else in the directory (a
// segment still being copied, or a file the tenant put there) is left alone.
var walFileName = regexp.MustCompile(`^([0-9A-F]{24}|[0-9A-F]{8}\.history|[0-9A-F]{24}\.[0-9A-F]{8}\.backup)$`)

// ErrNoWALArchive is a project with backups whose container has nowhere
// archive_command writes: nothing it writes reaches its backups.
var ErrNoWALArchive = errors.New("the project's database has no WAL archive")

// ShipWAL moves the project's closed WAL segments into the backup store and
// removes them from its container (EXC-593).
func (a *DockerBackupAdapter) ShipWAL(ctx context.Context, inst *domain.DatabaseInstance) error {
	return a.shipWAL(ctx, inst, false)
}

// RefreshWALArchive closes the current segment and ships it with the rest.
func (a *DockerBackupAdapter) RefreshWALArchive(ctx context.Context, inst *domain.DatabaseInstance) error {
	return a.shipWAL(ctx, inst, true)
}

func (a *DockerBackupAdapter) shipWAL(ctx context.Context, inst *domain.DatabaseInstance, closeCurrent bool) error {
	a.mu.RLock()
	dc := a.docker
	a.mu.RUnlock()
	if dc == nil {
		return fmt.Errorf("docker client not configured")
	}
	if inst.Namespace == "" {
		return fmt.Errorf("no container id on instance")
	}
	a.shipping.Lock()
	defer a.shipping.Unlock()
	if closeCurrent {
		switchWAL(ctx, dc, inst)
	}
	shipped, err := a.uploadArchived(ctx, dc, inst)
	if len(shipped) > 0 {
		if clearErr := clearShipped(ctx, dc, inst.Namespace, shipped); clearErr != nil {
			err = errors.Join(err, clearErr)
		}
	}
	return err
}

// switchWAL closes the segment holding the latest commits so it is archived.
func switchWAL(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) {
	dbName := inst.DatabaseName
	if dbName == "" {
		dbName = "postgres"
	}
	if _, err := dc.ExecInContainer(ctx, inst.Namespace,
		[]string{"psql", "-U", "postgres", "-d", dbName, "-c", "SELECT pg_switch_wal();"}); err != nil {
		log.Printf("WARN: switch WAL of %s: %v", inst.ProjectID, err)
	}
}

// uploadArchived uploads each archived file and names those that are in the
// store, including when a later one failed.
func (a *DockerBackupAdapter) uploadArchived(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) ([]string, error) {
	stream, err := dc.CopyFromContainer(ctx, inst.Namespace, walArchivePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoWALArchive, err)
	}
	defer stream.Close()
	prefix := fmt.Sprintf("%s%s/wals/", a.keyPrefix, inst.ProjectID)
	reader := tar.NewReader(stream)
	var shipped []string
	for {
		hdr, err := reader.Next()
		if err == io.EOF {
			return shipped, nil
		}
		if err != nil {
			return shipped, fmt.Errorf("read the WAL archive: %w", err)
		}
		name := walSegmentName(hdr)
		if !walFileName.MatchString(name) {
			continue
		}
		if err := a.uploadOneWAL(ctx, reader, prefix, name); err != nil {
			return shipped, err
		}
		shipped = append(shipped, name)
	}
}

// clearShipped removes shipped files from the container's archive.
func clearShipped(ctx context.Context, dc provisioner.DockerClient, container string, names []string) error {
	cmd := []string{"rm", "-f", "--"}
	for _, name := range names {
		cmd = append(cmd, walArchivePath+"/"+name)
	}
	code, err := dc.ExecInContainer(ctx, container, cmd)
	if err != nil {
		return fmt.Errorf("clear shipped WAL: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("clear shipped WAL: rm exited %d", code)
	}
	return nil
}

// walSegmentName returns the base file name for a regular-file tar header, or
// "" for non-regular entries (directories, etc.) that should be skipped.
func walSegmentName(hdr *tar.Header) string {
	if hdr.Typeflag != tar.TypeReg {
		return ""
	}
	name := hdr.Name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// uploadOneWAL gzips a single WAL segment read from reader and uploads it
// under prefix+name+".gz".
func (a *DockerBackupAdapter) uploadOneWAL(ctx context.Context, reader io.Reader, prefix, name string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read WAL %s: %w", name, err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("gzip WAL %s: %w", name, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("gzip WAL %s: %w", name, err)
	}
	if _, err := a.uploader.Upload(ctx, a.bucket, prefix+name+".gz", &compressed); err != nil {
		return fmt.Errorf("upload WAL %s: %w", name, err)
	}
	return nil
}

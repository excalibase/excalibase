package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// WALArchiveDir is where archive_command leaves each closed WAL segment for
// the platform to ship to the backup store (EXC-593).
const WALArchiveDir = "/walarchive"

// walArchiveCommand renames a segment into place, so whatever reads the
// directory never takes half of one.
const walArchiveCommand = "cp %p " + WALArchiveDir + "/%f.part && mv " + WALArchiveDir + "/%f.part " + WALArchiveDir + "/%f"

// walArchiveTimeout closes a segment that has had writes this long, so a
// quiet project's last writes still reach the store.
const walArchiveTimeout = "5min"

// ArchiveTimeoutOf is the archive_timeout a project on tier runs: the plan's,
// or the five minutes a plan that names none keeps (EXC-784).
func ArchiveTimeoutOf(tier config.TierConfig) string {
	if tier.ArchiveTimeout != "" {
		return tier.ArchiveTimeout
	}
	return walArchiveTimeout
}

// PostgresImageOwner is the official postgres image's postgres user.
var PostgresImageOwner = FileOwner{UID: 999, GID: 999}

// enableArchive turns WAL archiving on for a project that has backups; one
// that must have them and cannot archive is not handed out.
func (p *DockerPostgreSQLProvisioner) enableArchive(ctx context.Context, req domain.ProvisioningRequest, containerID, dbName string, owner FileOwner, timeout time.Duration, tier config.TierConfig) error {
	if req.Backup == nil || !req.Backup.Enabled {
		return nil
	}
	if err := p.EnableWALArchive(ctx, containerID, owner, ArchiveTimeoutOf(tier)); err != nil {
		return fmt.Errorf("enable WAL archiving: %w", err)
	}
	if err := p.waitForPostgresReady(ctx, containerID, dbName, timeout); err != nil {
		return fmt.Errorf("wait for postgres after enabling WAL archiving: %w", err)
	}
	return nil
}

// EnableWALArchive gives the container an archive directory its server
// (owner) writes, and archives every closed segment into it, closing a quiet
// one after archiveTimeout ("" keeps five minutes). It restarts the container;
// the caller waits for postgres again.
func (p *DockerPostgreSQLProvisioner) EnableWALArchive(ctx context.Context, containerID string, owner FileOwner, archiveTimeout string) error {
	directory, err := archiveDirTar(owner)
	if err != nil {
		return err
	}
	if err := p.docker.CopyToContainer(ctx, containerID, "/", bytes.NewReader(directory)); err != nil {
		return fmt.Errorf("create %s: %w", WALArchiveDir, err)
	}
	return p.ConfigureArchive(ctx, containerID, walArchiveCommand, defaultPostgresSuperuser, archiveTimeout)
}

func archiveDirTar(owner FileOwner) ([]byte, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	header := &tar.Header{Name: "walarchive/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: owner.UID, Gid: owner.GID, ModTime: time.Now()}
	if err := writer.WriteHeader(header); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ConfigureArchive enables continuous WAL archiving on the project's
// postgres container. Must be called *after* the container is up and
// pg is query-ready. The container is restarted at the end because
// `archive_mode` is changed only on (re)start, not on reload.
func (p *DockerPostgreSQLProvisioner) ConfigureArchive(ctx context.Context, containerID, archiveCommand, superuser, archiveTimeout string) error {
	if archiveTimeout == "" {
		archiveTimeout = walArchiveTimeout
	}
	// ALTER SYSTEM refuses a transaction block, and psql runs a single -c
	// string as one: each setting gets a -c of its own.
	cmd := []string{"psql", "-U", superuser, "-v", "ON_ERROR_STOP=1"}
	for _, setting := range []string{
		// Logical, not replica: realtime decodes this WAL too (EXC-612).
		"wal_level = 'logical'",
		"archive_mode = 'on'",
		"archive_command = " + quoteSQLString(archiveCommand),
		"archive_timeout = " + quoteSQLString(archiveTimeout),
	} {
		cmd = append(cmd, "-c", "ALTER SYSTEM SET "+setting)
	}
	code, err := p.docker.ExecInContainer(ctx, containerID, cmd)
	if err != nil {
		return fmt.Errorf("psql ALTER SYSTEM: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("psql ALTER SYSTEM exited %d", code)
	}
	// archive_mode is read at startup only — reload won't pick it up.
	if err := p.docker.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("stop for archive restart: %w", err)
	}
	if err := p.StartHoldingPort(ctx, containerID); err != nil {
		return fmt.Errorf("start after archive ALTER: %w", err)
	}
	return nil
}

// quoteSQLString single-quote-escapes a string for safe inclusion in
// an ALTER SYSTEM SET statement. archive_command is operator-controlled
// (not user input) but we still want to avoid surprising breakage if
// the value contains a single quote — wrap in '...' and double any
// embedded quote per Postgres lexical rules.
func quoteSQLString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

// SetArchiveTimeout moves a running project's archive_timeout to want, and
// reports whether it had to. The setting reloads, so the container is not
// restarted (EXC-784).
func (p *DockerPostgreSQLProvisioner) SetArchiveTimeout(ctx context.Context, containerID, want string) (bool, error) {
	psql := []string{"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-U", defaultPostgresSuperuser, "-d", "postgres", "-f", "-"}
	current, err := p.docker.ExecInContainerStdin(ctx, containerID, psql, "SHOW archive_timeout;")
	if err != nil {
		return false, fmt.Errorf("read archive_timeout: %w", err)
	}
	if strings.TrimSpace(current) == want {
		return false, nil
	}
	apply := "ALTER SYSTEM SET archive_timeout = " + quoteSQLString(want) + ";\nSELECT pg_reload_conf();\n"
	if _, err := p.docker.ExecInContainerStdin(ctx, containerID, psql, apply); err != nil {
		return false, fmt.Errorf("set archive_timeout: %w", err)
	}
	return true, nil
}

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// WALGBackupRunner implements BackupRunner against a wal-g sidecar
// container. The sidecar must already be running alongside the
// project's postgres container (set up by DockerPostgreSQLProvisioner
// at provision time).
//
// Phase 2 contract:
//   - BasebackupTo invokes `wal-g backup-push` inside the sidecar; the
//     sidecar streams the archive to S3 directly. To preserve the
//     io.Writer interface, BasebackupTo writes the *manifest* (small —
//     includes the backup name and size) into dst so callers can
//     record it in the BackupRecord row. The actual bytes never
//     transit the platform process.
//   - RestoreFrom is a no-op for now: Phase 3's restore orchestrator
//     pre-stages the data dir using wal-g backup-fetch from inside a
//     freshly-created restore container; the io.Reader contract is
//     not the right shape there.
type WALGBackupRunner struct {
	sdk     dockerSDK
	sidecar string // sidecar container name, e.g. "excalibase-{projectId}-walg"
}

// WALGBackupRunnerConfig wires the runner.
type WALGBackupRunnerConfig struct {
	Client      *client.Client
	SidecarName string // container name pattern resolves the sidecar by inst.ProjectID
}

// NewWALGBackupRunner wraps a real Docker SDK client. SidecarName is
// the *literal* sidecar container name; for production each project
// has its own, named after its ProjectID via SidecarContainerName().
func NewWALGBackupRunner(c *client.Client, sidecarName string) *WALGBackupRunner {
	return &WALGBackupRunner{sdk: &realDockerSDK{c: c}, sidecar: sidecarName}
}

// SidecarContainerName is the canonical name for a project's WAL-G
// sidecar. Mirrors the pg container naming pattern in
// docker_postgresql.go.
func SidecarContainerName(projectID string) string {
	return fmt.Sprintf("excalibase-%s-walg", projectID)
}

func (r *WALGBackupRunner) BasebackupTo(ctx context.Context, inst *domain.DatabaseInstance, dst io.Writer) error {
	// `wal-g backup-push /var/lib/postgresql/data` runs inside the
	// sidecar against the shared volume. We capture the manifest line
	// (last line of stdout — wal-g prints "Backup base_<name> sucessfully [sic] written") so
	// the caller can persist it.
	cmd := []string{"wal-g", "backup-push", "/var/lib/postgresql/data"}

	exec, err := r.sdk.ContainerExecCreate(ctx, r.sidecar, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Env:          inst.WALGEnv(),
	})
	if err != nil {
		return fmt.Errorf("walg exec create: %w", err)
	}

	att, err := r.sdk.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Detach: false})
	if err != nil {
		return fmt.Errorf("walg exec attach: %w", err)
	}
	if att.Closer != nil {
		defer att.Closer.Close()
	}

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return fmt.Errorf("walg stream: %w (stderr: %s)", err, truncate(stderr.String(), 512))
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		insp, err := r.sdk.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return fmt.Errorf("walg exec inspect: %w", err)
		}
		if !insp.Running {
			if insp.ExitCode != 0 {
				return fmt.Errorf("wal-g backup-push exited %d (stderr: %s)", insp.ExitCode, truncate(stderr.String(), 512))
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Persist the manifest summary. dst gets a single human-readable
	// line so the BackupRecord can show it without bloating the row.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = "wal-g backup-push completed"
	}
	if _, err := io.WriteString(dst, out); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func (r *WALGBackupRunner) RestoreFrom(_ context.Context, _ *domain.DatabaseInstance, _ io.Reader) error {
	// Restore in WAL-G mode is driven by the Phase 3 orchestrator,
	// which calls `wal-g backup-fetch` inside a fresh sidecar against
	// a newly-provisioned (empty, stopped) postgres container.
	return fmt.Errorf("walg restore via runner not supported; use restore orchestrator")
}

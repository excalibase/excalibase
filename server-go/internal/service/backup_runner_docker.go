package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// dockerSDK is the subset of *docker/client.Client we lean on.
// Defined here so tests can fake it without depending on the
// upstream SDK surface.
type dockerSDK interface {
	ContainerExecCreate(ctx context.Context, container string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, options container.ExecStartOptions) (resp dockerHijackedResponse, err error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// dockerHijackedResponse mirrors types.HijackedResponse with only the
// fields we use, keeping the test surface tiny.
type dockerHijackedResponse struct {
	Reader io.Reader
	Closer io.Closer
}

// realDockerSDK adapts the live *client.Client to dockerSDK.
type realDockerSDK struct{ c *client.Client }

func (r *realDockerSDK) ContainerExecCreate(ctx context.Context, id string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	return r.c.ContainerExecCreate(ctx, id, options)
}

func (r *realDockerSDK) ContainerExecAttach(ctx context.Context, execID string, options container.ExecStartOptions) (dockerHijackedResponse, error) {
	resp, err := r.c.ContainerExecAttach(ctx, execID, options)
	if err != nil {
		return dockerHijackedResponse{}, err
	}
	return dockerHijackedResponse{Reader: resp.Reader, Closer: resp.Conn}, nil
}

func (r *realDockerSDK) ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error) {
	return r.c.ContainerExecInspect(ctx, execID)
}

// DockerBackupRunner is the production BackupRunner. It uses
// `docker exec` to run pg_basebackup inside the project's postgres
// container and streams the resulting tar to dst.
type DockerBackupRunner struct {
	sdk dockerSDK
}

// NewDockerBackupRunner wraps a real *client.Client.
func NewDockerBackupRunner(c *client.Client) *DockerBackupRunner {
	return &DockerBackupRunner{sdk: &realDockerSDK{c: c}}
}

// BasebackupTo runs `pg_basebackup -F t -X fetch -D - -P` inside the
// project's postgres container as the postgres superuser. Output is
// the standard pg_basebackup tar stream piped into dst.
//
// The container ID lives in inst.Namespace (DockerProvisioner stamps
// the container ID there at provision time — see
// docker_postgresql.go:102). The postgres password is the platform-
// generated superuser secret stored in inst.Password.
func (r *DockerBackupRunner) BasebackupTo(ctx context.Context, inst *domain.DatabaseInstance, dst io.Writer) error {
	if inst.Namespace == "" {
		return fmt.Errorf("instance %s has no container id", inst.ProjectID)
	}
	cmd := []string{
		"pg_basebackup",
		"-U", "postgres",
		"-D", "-",
		"-F", "tar",
		"-X", "fetch",
		"-z",
	}
	exec, err := r.sdk.ContainerExecCreate(ctx, inst.Namespace, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Env:          []string{"PGPASSWORD=" + inst.Password},
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	att, err := r.sdk.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Detach: false})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	if att.Closer != nil {
		defer att.Closer.Close()
	}

	// Docker's exec attach stream is multiplexed (8-byte header per chunk
	// labelling stdout vs stderr) when TTY is off. stdcopy.StdCopy splits
	// it; we route stdout (the basebackup tar) to dst and stderr to a
	// bounded buffer for error reporting.
	var stderr bytes.Buffer
	copyErr := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(dst, &stderr, att.Reader)
		copyErr <- err
	}()

	select {
	case err := <-copyErr:
		if err != nil {
			return fmt.Errorf("read backup stream: %w (stderr: %s)", err, truncate(stderr.String(), 512))
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// Confirm pg_basebackup exited 0 — non-zero means the tar we just
	// wrote is incomplete and must not be marked COMPLETED upstream.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		insp, err := r.sdk.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return fmt.Errorf("exec inspect: %w", err)
		}
		if !insp.Running {
			if insp.ExitCode != 0 {
				return fmt.Errorf("pg_basebackup exited %d", insp.ExitCode)
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("pg_basebackup exec did not exit within 30s")
}

// RestoreFrom is the symmetric op: stream `src` (a pg_basebackup tar)
// into the postgres data dir of a freshly-created restore container.
// Phase 1 exposes the surface; the actual implementation lands with
// Phase 3's restore orchestrator (a fresh container is provisioned
// stopped, the tar is unpacked into PGDATA, then it's started).
func (r *DockerBackupRunner) RestoreFrom(_ context.Context, _ *domain.DatabaseInstance, _ io.Reader) error {
	return fmt.Errorf("docker restore not implemented in Phase 1")
}

// truncate returns s clipped to n bytes for safe inclusion in error
// messages. Used so a runaway stderr from pg_basebackup doesn't bloat
// log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

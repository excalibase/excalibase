package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeDockerSDK lets us drive the runner without a real daemon.
type fakeDockerSDK struct {
	createCalls int
	cmdSeen     []string
	envSeen     []string
	stdout      []byte
	stderr      []byte
	exitCode    int
	createErr   error
	attachErr   error
}

func (f *fakeDockerSDK) ContainerExecCreate(_ context.Context, _ string, opts container.ExecOptions) (container.ExecCreateResponse, error) {
	f.createCalls++
	f.cmdSeen = opts.Cmd
	f.envSeen = opts.Env
	if f.createErr != nil {
		return container.ExecCreateResponse{}, f.createErr
	}
	return container.ExecCreateResponse{ID: "exec-1"}, nil
}

func (f *fakeDockerSDK) ContainerExecAttach(_ context.Context, _ string, _ container.ExecStartOptions) (dockerHijackedResponse, error) {
	if f.attachErr != nil {
		return dockerHijackedResponse{}, f.attachErr
	}
	// Encode in Docker stream format: 1 byte stream type, 3 zero bytes,
	// 4-byte big-endian length, then the payload.
	out := encodeStream(1, f.stdout)
	errStream := encodeStream(2, f.stderr)
	return dockerHijackedResponse{
		Reader: io.MultiReader(bytes.NewReader(out), bytes.NewReader(errStream)),
		Closer: io.NopCloser(nil),
	}, nil
}

func (f *fakeDockerSDK) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	return container.ExecInspect{
		Running:  false,
		ExitCode: f.exitCode,
	}, nil
}

func encodeStream(streamType byte, payload []byte) []byte {
	header := []byte{streamType, 0, 0, 0, 0, 0, 0, 0}
	n := len(payload)
	header[4] = byte(n >> 24)
	header[5] = byte(n >> 16)
	header[6] = byte(n >> 8)
	header[7] = byte(n)
	return append(header, payload...)
}

func TestWALGRunner_BasebackupTo_ExecsBackupPush(t *testing.T) {
	fake := &fakeDockerSDK{
		stdout:   []byte("Backup base_000000010000000000000003 successfully written\n"),
		exitCode: 0,
	}
	r := &WALGBackupRunner{sdk: fake, sidecar: "excalibase-p1-walg"}

	var dst bytes.Buffer
	if err := r.BasebackupTo(context.Background(), &domain.DatabaseInstance{ProjectID: "p1"}, &dst); err != nil {
		t.Fatalf("BasebackupTo: %v", err)
	}
	if fake.createCalls != 1 {
		t.Errorf("create calls: got %d", fake.createCalls)
	}
	if fake.cmdSeen[0] != "wal-g" || fake.cmdSeen[1] != "backup-push" {
		t.Errorf("cmd: %v", fake.cmdSeen)
	}
	got := dst.String()
	if !strings.Contains(got, "Backup base_") {
		t.Errorf("manifest missing wal-g output: %q", got)
	}
}

func TestWALGRunner_NonZeroExit_ReturnsError(t *testing.T) {
	fake := &fakeDockerSDK{
		stdout:   []byte(""),
		stderr:   []byte("ERROR: cannot connect to bucket\n"),
		exitCode: 2,
	}
	r := &WALGBackupRunner{sdk: fake, sidecar: "x"}
	var dst bytes.Buffer
	err := r.BasebackupTo(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, &dst)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "exited 2") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWALGRunner_AttachError_Propagates(t *testing.T) {
	fake := &fakeDockerSDK{attachErr: errors.New("daemon unreachable")}
	r := &WALGBackupRunner{sdk: fake, sidecar: "x"}
	err := r.BasebackupTo(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("expected attach error, got %v", err)
	}
}

func TestWALGRunner_RestoreFrom_NotImplemented(t *testing.T) {
	r := &WALGBackupRunner{sdk: &fakeDockerSDK{}, sidecar: "x"}
	err := r.RestoreFrom(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, strings.NewReader(""))
	if err == nil {
		t.Error("expected RestoreFrom to error — Phase 3 owns restore")
	}
}

func TestSidecarContainerName_FollowsConvention(t *testing.T) {
	got := SidecarContainerName("proj-abc123")
	want := "excalibase-proj-abc123-walg"
	if got != want {
		t.Errorf("sidecar name: got %q, want %q", got, want)
	}
}

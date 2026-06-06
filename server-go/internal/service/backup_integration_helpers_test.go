//go:build integration

package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// startArchivingPostgres boots a postgres:17 container with /walarchive
// pre-created (owned by postgres) and archive_mode enabled, restarting once so
// the settings take effect. Returns the container handle and its container ID.
// Extracted so the PITR tests share one setup path with low cognitive load.
func startArchivingPostgres(ctx context.Context, t *testing.T, pgPwd string) (testcontainers.Container, string) {
	t.Helper()
	pgC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:17",
			Env: map[string]string{
				"POSTGRES_DB":       "app",
				"POSTGRES_USER":     "postgres",
				"POSTGRES_PASSWORD": pgPwd,
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start source postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	srcID := pgC.GetContainerID()

	mkdir := exec.CommandContext(ctx, "docker", "exec", "-u", "0", srcID,
		"sh", "-c", "mkdir -p /walarchive && chown postgres:postgres /walarchive && chmod 700 /walarchive")
	if out, err := mkdir.CombinedOutput(); err != nil {
		t.Fatalf("mkdir /walarchive: %v: %s", err, out)
	}
	for _, sql := range []string{
		"ALTER SYSTEM SET wal_level = 'replica';",
		"ALTER SYSTEM SET archive_mode = 'on';",
		"ALTER SYSTEM SET archive_command = 'cp %p /walarchive/%f';",
	} {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, sql); err != nil {
			t.Fatalf("alter system %q: %v", sql, err)
		}
	}
	restartPostgres(ctx, t, pgC)
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CHECKPOINT"); err != nil {
		t.Fatalf("pre-backup checkpoint: %v", err)
	}
	return pgC, srcID
}

// restartPostgres stops then starts the container and waits for it to settle,
// used after ALTER SYSTEM changes that require a restart.
func restartPostgres(ctx context.Context, t *testing.T, pgC testcontainers.Container) {
	t.Helper()
	if err := pgC.Stop(ctx, nil); err != nil {
		t.Fatalf("stop postgres for restart: %v", err)
	}
	if err := pgC.Start(ctx); err != nil {
		t.Fatalf("start postgres after restart: %v", err)
	}
	time.Sleep(3 * time.Second)
}

// newLocalStackUploader starts a LocalStack S3 container, builds an uploader
// pointed at it, and creates the named bucket. Returns the ready uploader.
func newLocalStackUploader(ctx context.Context, t *testing.T, bucket string) *AWSS3Uploader {
	t.Helper()
	lsC, err := tclocalstack.Run(ctx, "localstack/localstack:3.7",
		testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	if err != nil {
		t.Fatalf("start localstack: %v", err)
	}
	t.Cleanup(func() { lsC.Terminate(ctx) })
	lsHost, _ := lsC.Host(ctx)
	lsPort, _ := lsC.MappedPort(ctx, "4566/tcp")
	endpoint := fmt.Sprintf("http://%s:%s", lsHost, lsPort.Port())

	uploader, err := NewAWSS3Uploader(ctx, AWSS3UploaderConfig{
		AccessKeyID: "test", SecretAccessKey: "test", Region: "us-east-1",
		Endpoint: endpoint, UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("uploader: %v", err)
	}
	if _, err := uploader.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return uploader
}

// newRestoreAdapter wires a DockerBackupAdapter with a real docker SDK runner +
// real docker client for restore, backed by a filesystem instance store and an
// in-memory record store. Returns the adapter and its store.
func newRestoreAdapter(ctx context.Context, t *testing.T, uploader S3Uploader, bucket string) (*DockerBackupAdapter, *storage.FileSystemStore) {
	t.Helper()
	dockerSDK, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker SDK client: %v", err)
	}
	t.Cleanup(func() { dockerSDK.Close() })
	runner := NewDockerBackupRunner(dockerSDK)

	realDocker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Fatalf("real docker client: %v", err)
	}

	storeDir := t.TempDir()
	store, _ := storage.NewFileSystemStore(storeDir)
	records := &fakeBackupRecordStore{}

	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner: runner, Uploader: uploader, Records: records,
		Bucket: bucket, KeyPrefix: "backups/", Instances: store,
	})
	adapter.SetDockerClient(realDocker)
	return adapter, store
}

// waitForSmokeRows polls the restored container until `SELECT id FROM smoke
// ORDER BY id` matches wantRows, or fails after the deadline dumping diagnostics.
func waitForSmokeRows(ctx context.Context, t *testing.T, containerID, password, label, wantRows string, timeout time.Duration) {
	t.Helper()
	if waitForQueryOK(ctx, containerID, password, "SELECT id FROM smoke ORDER BY id", wantRows, timeout) {
		return
	}
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "60", containerID).CombinedOutput()
	t.Logf("=== %s container logs (tail=60) ===\n%s", label, string(logs))
	t.Fatalf("PITR (%s) did not reach target rows=%q", label, wantRows)
}

// waitForQueryResult polls a query until its trimmed output equals want, failing
// the test (with the last output) once timeout elapses.
func waitForQueryResult(ctx context.Context, t *testing.T, containerID, password, query, want string, timeout time.Duration) {
	t.Helper()
	if waitForQueryOK(ctx, containerID, password, query, want, timeout) {
		return
	}
	t.Fatalf("query %q never returned %q within %s", query, want, timeout)
}

// waitForQueryOK runs query repeatedly until its trimmed stdout equals want or
// the timeout elapses, returning whether it matched. Shared poll loop so the
// public wait helpers stay flat.
func waitForQueryOK(ctx context.Context, containerID, password, query, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := dockerExecPSQLQuery(ctx, containerID, password, query)
		if err == nil && strings.TrimSpace(got) == want {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

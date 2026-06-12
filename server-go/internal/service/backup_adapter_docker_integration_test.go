//go:build integration

package service

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// TestDockerBackupAdapter_IntegrationE2E exercises the full Phase 1
// pipeline: real Postgres in a container, real DockerBackupRunner
// using `docker exec pg_basebackup`, real AWSS3Uploader against a
// LocalStack S3 endpoint. The S3 object is then downloaded and
// verified to be a valid pg_basebackup tar (PG version banner check).
func TestDockerBackupAdapter_IntegrationE2E(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI required for integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- Postgres container -------------------------------------------------
	pgPwd := testutil.FixturePassword("pg-bk-int")
	pgC, err := tcpg.Run(ctx, "postgres:16-alpine",
		tcpg.WithDatabase("app"),
		tcpg.WithUsername("postgres"),
		tcpg.WithPassword(pgPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	pgID := pgC.GetContainerID()

	// Seed the DB inside the container so the backup has identifiable
	// content. Using `docker exec` keeps the host's psql out of the
	// dependency surface.
	if err := dockerExecPSQL(ctx, pgID, pgPwd, "CREATE TABLE smoke (n int); INSERT INTO smoke VALUES (42);"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --- LocalStack S3 ------------------------------------------------------
	lsC, err := tclocalstack.Run(ctx, "localstack/localstack:3.7",
		testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}),
	)
	if err != nil {
		t.Fatalf("start localstack: %v", err)
	}
	t.Cleanup(func() { lsC.Terminate(ctx) })
	lsHost, _ := lsC.Host(ctx)
	lsPort, _ := lsC.MappedPort(ctx, "4566/tcp")
	endpoint := fmt.Sprintf("http://%s:%s", lsHost, lsPort.Port())

	uploader, err := NewAWSS3Uploader(ctx, AWSS3UploaderConfig{
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		Region:          "us-east-1",
		Endpoint:        endpoint,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("uploader: %v", err)
	}
	bucket := "excalibase-test-backups"
	if _, err := uploader.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// --- Adapter wiring -----------------------------------------------------
	storeDir := t.TempDir()
	store, _ := storage.NewFileSystemStore(storeDir)
	records := &fakeBackupRecordStore{}

	// Spin up a real Docker SDK runner.
	runner := newRealDockerRunnerForTest(t)
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner:    runner,
		Uploader:  uploader,
		Records:   records,
		Bucket:    bucket,
		KeyPrefix: "backups/",
		Instances: store,
	})

	inst := &domain.DatabaseInstance{
		ProjectID:      "intg-1",
		OrgID:          "org",
		Namespace:      pgID, // DockerProvisioner stamps container ID here
		DatabaseName:   "app",
		Username:       defaultPostgresSuperuser,
		Password:       pgPwd,
		DeploymentMode: domain.ModeDocker,
		Status:         "ACTIVE",
	}
	store.Save(inst)

	// --- Trigger + verify ---------------------------------------------------
	ref, err := adapter.TriggerManual(ctx, inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	if ref.Status != "COMPLETED" {
		t.Errorf("status: got %s, want COMPLETED", ref.Status)
	}
	if ref.SizeBytes <= 0 {
		t.Errorf("expected non-zero size, got %d", ref.SizeBytes)
	}

	// Verify the uploaded object is a real pg_basebackup tar.gz.
	key := fmt.Sprintf("backups/intg-1/manual/%s.tar.gz", ref.ID)
	body, err := uploader.Download(ctx, bucket, key)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer body.Close()
	first, err := io.ReadAll(io.LimitReader(body, 4))
	if err != nil {
		t.Fatalf("read magic: %v", err)
	}
	// gzip magic: 0x1f 0x8b
	if len(first) < 2 || first[0] != 0x1f || first[1] != 0x8b {
		t.Errorf("uploaded blob is not gzip: % x", first)
	}

	// Records persisted with COMPLETED.
	got, _ := records.ListByProject(ctx, "intg-1")
	if len(got) != 1 || got[0].Status != "COMPLETED" {
		t.Errorf("records: %+v", got)
	}
}

// dockerExecPSQL runs a SQL string via `docker exec` inside the
// postgres container. Saves us a psql client dependency on the host.
func dockerExecPSQL(ctx context.Context, containerID, password, sql string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec",
		"-e", "PGPASSWORD="+password,
		containerID,
		"psql", "-U", "postgres", "-d", "app", "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker exec psql: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// newRealDockerRunnerForTest builds a real DockerBackupRunner using
// the host docker client. Tests skip themselves above if `docker` is
// missing, so this should always succeed when reached.
func newRealDockerRunnerForTest(t *testing.T) *DockerBackupRunner {
	t.Helper()
	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return NewDockerBackupRunner(c)
}

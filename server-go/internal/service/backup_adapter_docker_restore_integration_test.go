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
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// TestDockerBackupAdapter_RestoreE2E proves the full Docker-mode
// backup → S3 → restore → query round-trip. Until this passed,
// the Restore unit tests only proved structural wiring (fake
// docker client + drained byte stream); they couldn't prove the
// restored container actually opens with the seeded data.
//
// Pipeline under test:
//
//   1. Seed source postgres with `CREATE TABLE smoke; INSERT 4242`
//   2. DockerBackupAdapter.TriggerManual → S3 (LocalStack)
//   3. DockerBackupAdapter.Restore — creates a fresh container,
//      gunzips + CopyToContainer the tar into /var/lib/postgresql/data,
//      starts it, waits healthy
//   4. Query the restored container: `SELECT n FROM smoke` must
//      return 4242 (proves the data is the source's, not initdb's)
//
// What this would catch that the unit tests don't:
//   - File ownership / mode mismatch between docker cp and pg's
//     PGDATA expectations (pg refuses 0755; needs 0700 + postgres:postgres)
//   - Tar layout from pg_basebackup that doesn't unpack into PGDATA root
//   - WaitForHealthy returning before pg actually accepts queries
//   - Missing pg_wal contents that initdb would have created
//
// Skips when docker is unavailable.
func TestDockerBackupAdapter_RestoreE2E(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- Source postgres + seed -------------------------------------------
	pgPwd := testutil.FixturePassword("pg-restore-int")
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
		t.Fatalf("start source postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	srcID := pgC.GetContainerID()

	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CREATE TABLE smoke (n int); INSERT INTO smoke VALUES (4242);"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	// Force a checkpoint so the basebackup captures the row.
	_ = dockerExecPSQL(ctx, srcID, pgPwd, "CHECKPOINT;")

	// --- LocalStack S3 ----------------------------------------------------
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
	bucket := "excalibase-restore-e2e"
	if _, err := uploader.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// --- Real docker SDK + adapter wiring ---------------------------------
	dockerSDK, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
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
		Runner:    runner,
		Uploader:  uploader,
		Records:   records,
		Bucket:    bucket,
		KeyPrefix: "backups/",
		Instances: store,
	})
	adapter.SetDockerClient(realDocker)

	src := &domain.DatabaseInstance{
		ProjectID:      "src-restore",
		OrgID:          "org",
		Namespace:      srcID,
		DatabaseName:   "app",
		Username:       "postgres",
		Password:       pgPwd,
		PostgresVersion: "16-alpine",
		DeploymentMode: domain.ModeDocker,
		Status:         "ACTIVE",
	}
	store.Save(src)

	// --- Trigger backup ---------------------------------------------------
	ref, err := adapter.TriggerManual(ctx, src)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	if ref.Status != "COMPLETED" {
		t.Fatalf("backup status: got %s, want COMPLETED", ref.Status)
	}
	t.Logf("backup completed: id=%s size=%d", ref.ID, ref.SizeBytes)

	// --- Restore -----------------------------------------------------------
	resp, err := adapter.Restore(ctx, src, domain.RestoreRequest{NewProjectID: "restored-001"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup of the restored container.
		_ = realDocker.StopContainer(context.Background(), resp.Namespace)
		_ = realDocker.RemoveContainer(context.Background(), resp.Namespace)
	})
	t.Logf("restore returned: project=%s container=%s host=%s", resp.ProjectID, resp.Namespace, resp.Host)

	// --- Verify restored DB has the source's data ------------------------
	// Wait a few seconds for postgres to finish recovery + accept queries.
	// WaitForHealthy returns on container "running" but the postgres
	// image has no HEALTHCHECK — pg may still be replaying WAL.
	queryDeadline := time.Now().Add(60 * time.Second)
	var lastErr error
	var got string
	for time.Now().Before(queryDeadline) {
		got, lastErr = dockerExecPSQLQuery(ctx, resp.Namespace, src.Password, "SELECT n FROM smoke")
		if lastErr == nil && strings.TrimSpace(got) == "4242" {
			return
		}
		time.Sleep(2 * time.Second)
	}
	// One final readable error.
	if lastErr != nil {
		t.Fatalf("restored DB never became queryable: lastErr=%v lastOut=%q", lastErr, got)
	}
	t.Fatalf("restored DB query returned unexpected value: got=%q want=4242", strings.TrimSpace(got))
}

// dockerExecPSQLQuery runs a SQL query and returns stdout.
func dockerExecPSQLQuery(ctx context.Context, containerID, password, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec",
		"-e", "PGPASSWORD="+password,
		containerID,
		"psql", "-U", "postgres", "-d", "app", "-tAc", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("psql query: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}


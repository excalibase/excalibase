//go:build integration

package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// TestDockerBackupAdapter_R2_E2E mirrors the LocalStack E2E but
// targets real Cloudflare R2. Gated on the same R2_* env names
// production reads; skips cleanly when unset.
//
// The LocalStack version proves the wiring; this version proves
// the path-style URLs survive R2's TLS SAN check, real multipart
// completes against R2's stricter etag handling, and the resulting
// blob is the gzipped tar we expect (gzip magic 0x1f 0x8b).
//
// Cost / cadence: a single 16MB-or-less backup object created and
// deleted per run. Cleanup runs in t.Cleanup so failures don't leak
// orphans into the bucket.
func TestDockerBackupAdapter_R2_E2E(t *testing.T) {
	ak := os.Getenv("R2_ACCESS_KEY_ID")
	sk := os.Getenv("R2_SECRET_ACCESS_KEY")
	endpoint := envFirst("R2_ENDPOINT", "BACKUP_DEFAULT_ENDPOINT")
	bucket := envFirst("R2_BUCKET", "BACKUP_DEFAULT_BUCKET")
	if ak == "" || sk == "" || endpoint == "" || bucket == "" {
		t.Skip("R2 creds not set — export R2_ACCESS_KEY_ID / R2_SECRET_ACCESS_KEY / R2_ENDPOINT / R2_BUCKET to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI required for integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- Postgres container -------------------------------------------------
	pgPwd := testutil.FixturePassword("pg-bk-r2")
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

	// Identifiable seed so the backup tar contains something specific.
	if err := dockerExecPSQL(ctx, pgID, pgPwd, "CREATE TABLE r2_marker (n int); INSERT INTO r2_marker VALUES (4242);"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --- Real R2 uploader ---------------------------------------------------
	// path-style ON: required for R2's shared TLS cert. Verified
	// against the storagesvc.R2Client invariant in r2_client.go:62.
	uploader, err := NewAWSS3Uploader(ctx, AWSS3UploaderConfig{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		Endpoint:        endpoint,
		Region:          "auto",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("uploader: %v", err)
	}

	// --- Adapter wiring -----------------------------------------------------
	storeDir := t.TempDir()
	store, _ := storage.NewFileSystemStore(storeDir)
	records := &fakeBackupRecordStore{}

	dockerSDK, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { dockerSDK.Close() })
	runner := NewDockerBackupRunner(dockerSDK)

	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner:    runner,
		Uploader:  uploader,
		Records:   records,
		Bucket:    bucket,
		KeyPrefix: "integration-tests/backup-adapter-r2/",
		Instances: store,
	})

	inst := &domain.DatabaseInstance{
		ProjectID:      fmt.Sprintf("intg-r2-%d", time.Now().UnixNano()),
		OrgID:          "org",
		Namespace:      pgID,
		DatabaseName:   "app",
		Username:       defaultPostgresSuperuser,
		Password:       pgPwd,
		DeploymentMode: domain.ModeDocker,
		Status:         "ACTIVE",
	}
	store.Create(inst)

	// --- Trigger + verify ---------------------------------------------------
	ref, err := adapter.TriggerManual(ctx, inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}

	// Always clean up the uploaded object — even on later assertions.
	uploadedKey := fmt.Sprintf("integration-tests/backup-adapter-r2/%s/manual/%s.tar.gz", inst.ProjectID, ref.ID)
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		_ = uploader.Delete(cleanCtx, bucket, uploadedKey)
	})

	if ref.Status != "COMPLETED" {
		t.Errorf("status: got %s, want COMPLETED", ref.Status)
	}
	if ref.SizeBytes <= 0 {
		t.Errorf("expected non-zero size, got %d", ref.SizeBytes)
	}

	assertUploadedGzip(ctx, t, uploader, bucket, uploadedKey)
	assertKeyInList(ctx, t, uploader, bucket,
		fmt.Sprintf("integration-tests/backup-adapter-r2/%s/", inst.ProjectID), uploadedKey)

	// BackupRecord persisted as COMPLETED.
	got, _ := records.ListByProject(ctx, inst.ProjectID)
	if len(got) != 1 || got[0].Status != "COMPLETED" {
		t.Errorf("records: %+v", got)
	}
}

// assertUploadedGzip round-trips the object and verifies the gzip magic bytes.
// Uses a LimitReader so we only read the first 4 bytes (no multi-MB egress).
func assertUploadedGzip(ctx context.Context, t *testing.T, uploader S3Uploader, bucket, key string) {
	t.Helper()
	body, err := uploader.Download(ctx, bucket, key)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	first, err := io.ReadAll(io.LimitReader(body, 4))
	body.Close()
	if err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if len(first) < 2 || first[0] != 0x1f || first[1] != 0x8b {
		t.Errorf("uploaded blob is not gzip: % x", first)
	}
}

// assertKeyInList lists under prefix and fails if wantKey is absent.
func assertKeyInList(ctx context.Context, t *testing.T, uploader S3Uploader, bucket, prefix, wantKey string) {
	t.Helper()
	listed, err := uploader.List(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	keys := make([]string, 0, len(listed))
	for _, obj := range listed {
		if obj.Key == wantKey {
			return
		}
		keys = append(keys, obj.Key)
	}
	t.Errorf("key %q not in List under prefix %q; saw %v", wantKey, prefix, strings.Join(keys, ", "))
}

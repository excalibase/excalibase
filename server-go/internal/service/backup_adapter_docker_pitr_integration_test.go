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

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	tcgeneric "github.com/testcontainers/testcontainers-go"
)

// TestDockerBackupAdapter_PITR_TargetName proves end-to-end PITR: a
// named restore point created BEFORE backup, then more inserts AFTER
// the restore point but still in the basebackup window, and recovery
// stops at the named target leaving only pre-mark rows visible.
//
// Pipeline:
//
//   1. Provision postgres:17 with archive_mode=on; archive_command
//      writes WALs to /walarchive (created via mkdir + chown postgres).
//   2. Seed timeline: row1 → CHECKPOINT → restore point 'mark' →
//      row2 → row3 → pg_switch_wal() → CHECKPOINT (closes the WAL
//      containing rows 2–3 + the mark, archive_command ships them).
//   3. TriggerManual: pg_basebackup → S3, plus uploadWALArchive() pulls
//      /walarchive contents → S3 wals/ prefix.
//   4. Restore with TargetName='mark': downloads WALs into pg_wal/,
//      writes recovery.signal + postgresql.auto.conf, starts postgres.
//   5. Recovery replays WALs, hits the mark, promotes.
//   6. Query: only row1 visible (rows 2/3 were after the mark).
//
// What this proves over the unit tests:
//
//   - archive_command path actually runs and produces WAL segments
//   - uploadWALArchive() reads /walarchive correctly via CopyFromContainer
//   - downloadWALsIntoContainer() places them under pg_wal/ with
//     correct ownership so postgres recovery can read them
//   - Postgres reaches a recovery target and promotes rather than hanging
func TestDockerBackupAdapter_PITR_TargetName(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	// --- Source postgres with /walarchive prepared -----------------------
	// Plain testcontainers (not the postgres module) — we need to
	// pre-create /walarchive and chown it to the postgres user before
	// the entrypoint runs initdb, otherwise archive_command will hit
	// permission errors.
	pgPwd := testutil.FixturePassword("pg-pitr-int")
	pgReq := tcgeneric.GenericContainerRequest{
		ContainerRequest: tcgeneric.ContainerRequest{
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
	}
	pgC, err := tcgeneric.GenericContainer(ctx, pgReq)
	if err != nil {
		t.Fatalf("start source postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	srcID := pgC.GetContainerID()

	// Create /walarchive owned by postgres so archive_command can write.
	mkdirCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "0", srcID,
		"sh", "-c", "mkdir -p /walarchive && chown postgres:postgres /walarchive && chmod 700 /walarchive")
	if out, err := mkdirCmd.CombinedOutput(); err != nil {
		t.Fatalf("mkdir /walarchive: %v: %s", err, out)
	}

	// Enable archive_mode + archive_command + restart. After restart,
	// every WAL switch produces a file in /walarchive.
	for _, sql := range []string{
		"ALTER SYSTEM SET wal_level = 'replica';",
		"ALTER SYSTEM SET archive_mode = 'on';",
		"ALTER SYSTEM SET archive_command = 'cp %p /walarchive/%f';",
	} {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, sql); err != nil {
			t.Fatalf("alter system %q: %v", sql, err)
		}
	}
	if err := pgC.Stop(ctx, nil); err != nil {
		t.Fatalf("stop source for archive restart: %v", err)
	}
	if err := pgC.Start(ctx); err != nil {
		t.Fatalf("start source post-archive: %v", err)
	}
	// Wait for the second "ready to accept connections" log line.
	time.Sleep(3 * time.Second)

	// Pre-backup: a clean DB. We'll do the seed activity AFTER the
	// basebackup so the WAL records (CREATE, INSERTs, restore point)
	// land in WALs that will be replayed during recovery.
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CHECKPOINT"); err != nil {
		t.Fatalf("pre-backup checkpoint: %v", err)
	}

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
		AccessKeyID: "test", SecretAccessKey: "test", Region: "us-east-1",
		Endpoint: endpoint, UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("uploader: %v", err)
	}
	bucket := "excalibase-pitr-e2e"
	if _, err := uploader.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// --- Adapter wiring ---------------------------------------------------
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

	src := &domain.DatabaseInstance{
		ProjectID: "src-pitr", OrgID: "org", Namespace: srcID,
		DatabaseName: "app", Username: "postgres", Password: pgPwd,
		PostgresVersion: "17", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	}
	store.Save(src)

	// --- Trigger backup of the empty DB ----------------------------------
	// This basebackup captures the initial empty state. Recovery on
	// restore starts from this backup's start_lsn — anything after
	// (the seeded inserts + restore point) gets replayed from
	// archived WALs.
	ref, err := adapter.TriggerManual(ctx, src)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	t.Logf("base backup id=%s", ref.ID)

	// --- Post-backup seed: this is what gets replayed during recovery ---
	timeline := []string{
		"CREATE TABLE smoke (id int PRIMARY KEY)",
		"INSERT INTO smoke VALUES (1)",
		"CHECKPOINT",
		"SELECT pg_create_restore_point('mark')",
		"SELECT pg_switch_wal()",
		"INSERT INTO smoke VALUES (2)",
		"INSERT INTO smoke VALUES (3)",
		"SELECT pg_switch_wal()",
		"CHECKPOINT",
	}
	for _, s := range timeline {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, s); err != nil {
			t.Fatalf("post-backup %q: %v", s, err)
		}
	}
	time.Sleep(2 * time.Second)

	// Ship the new WALs to S3 (out-of-band, no new basebackup).
	if err := adapter.RefreshWALArchive(ctx, src); err != nil {
		t.Fatalf("RefreshWALArchive: %v", err)
	}

	walObjects, err := uploader.List(ctx, bucket, "backups/src-pitr/wals/")
	if err != nil {
		t.Fatalf("list wals/: %v", err)
	}
	if len(walObjects) < 2 {
		t.Fatalf("expected ≥2 archived WAL segments after seed, got %d", len(walObjects))
	}
	t.Logf("archived %d WAL segment(s) post-seed", len(walObjects))

	// --- Restore from the EMPTY backup with TargetName="mark" -----------
	resp, err := adapter.Restore(ctx, src, domain.RestoreRequest{
		NewProjectID: "pitr-restored",
		BackupID:     ref.ID, // pin to the empty-state backup
		TargetName:   "mark",
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Cleanup(func() {
		_ = realDocker.StopContainer(context.Background(), resp.Namespace)
		_ = realDocker.RemoveContainer(context.Background(), resp.Namespace)
	})

	// --- Verify recovery stopped at the named target --------------------
	deadline := time.Now().Add(120 * time.Second)
	var lastOut string
	for time.Now().Before(deadline) {
		got, err := dockerExecPSQLQuery(ctx, resp.Namespace, src.Password, "SELECT id FROM smoke ORDER BY id")
		if err == nil {
			lastOut = strings.TrimSpace(got)
			if lastOut == "1" {
				return // PITR succeeded — only the pre-mark row is visible.
			}
		}
		time.Sleep(2 * time.Second)
	}
	// Diagnostic dump on failure: tail of postgres logs to surface
	// why recovery never finished (target not found / files unreadable
	// / waiting for next WAL / etc.).
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "60", resp.Namespace).CombinedOutput()
	t.Logf("=== restored container postgres logs (tail=60) ===\n%s", string(logs))
	dirList, _ := exec.CommandContext(ctx, "docker", "exec", resp.Namespace, "ls", "-la", "/var/lib/postgresql/data/pg_wal").CombinedOutput()
	t.Logf("=== pg_wal contents ===\n%s", string(dirList))
	t.Fatalf("PITR did not stop at restore point. Want rows=[1], got=%q", lastOut)
}

// TestDockerBackupAdapter_PITR_TargetTime + _TargetXID share the same
// pipeline shape as _TargetName; they swap the recovery target so we
// prove the recovery_target_time / recovery_target_xid directives are
// honored end-to-end (not just the directive serialisation, which the
// unit tests already cover).
//
// To keep CI cheap we run them under a single test that builds the
// source state once (postgres container + LocalStack + adapter
// wiring) and then sub-tests exercise each target kind by restoring
// into a fresh container. Each restore takes ~10–25s; total under a
// minute beyond TargetName itself.
func TestDockerBackupAdapter_PITR_TargetVariants(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// --- Source + LocalStack + adapter (same pattern as TargetName) ------
	pgPwd := testutil.FixturePassword("pg-pitr-variants")
	pgC, err := tcgeneric.GenericContainer(ctx, tcgeneric.GenericContainerRequest{
		ContainerRequest: tcgeneric.ContainerRequest{
			Image:        "postgres:17",
			Env:          map[string]string{"POSTGRES_DB": "app", "POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": pgPwd},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("source pg: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	srcID := pgC.GetContainerID()

	mkdir := exec.CommandContext(ctx, "docker", "exec", "-u", "0", srcID,
		"sh", "-c", "mkdir -p /walarchive && chown postgres:postgres /walarchive && chmod 700 /walarchive")
	if out, err := mkdir.CombinedOutput(); err != nil {
		t.Fatalf("mkdir /walarchive: %v: %s", err, out)
	}
	for _, sql := range []string{
		"ALTER SYSTEM SET wal_level='replica'",
		"ALTER SYSTEM SET archive_mode='on'",
		"ALTER SYSTEM SET archive_command='cp %p /walarchive/%f'",
	} {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, sql); err != nil {
			t.Fatalf("alter %q: %v", sql, err)
		}
	}
	pgC.Stop(ctx, nil)
	pgC.Start(ctx)
	time.Sleep(3 * time.Second)
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CHECKPOINT"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	lsC, err := tclocalstack.Run(ctx, "localstack/localstack:3.7",
		testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	if err != nil {
		t.Fatalf("localstack: %v", err)
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
	bucket := "excalibase-pitr-variants"
	if _, err := uploader.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	dockerSDK, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker SDK: %v", err)
	}
	t.Cleanup(func() { dockerSDK.Close() })
	runner := NewDockerBackupRunner(dockerSDK)
	rd, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Fatalf("real docker: %v", err)
	}

	storeDir := t.TempDir()
	store, _ := storage.NewFileSystemStore(storeDir)
	records := &fakeBackupRecordStore{}
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner: runner, Uploader: uploader, Records: records,
		Bucket: bucket, KeyPrefix: "backups/", Instances: store,
	})
	adapter.SetDockerClient(rd)

	src := &domain.DatabaseInstance{
		ProjectID: "src-variants", OrgID: "org", Namespace: srcID,
		DatabaseName: "app", Username: "postgres", Password: pgPwd,
		PostgresVersion: "17", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	}
	store.Save(src)

	// Take an empty-state backup we'll restore from for every variant.
	ref, err := adapter.TriggerManual(ctx, src)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}

	// Build the post-backup timeline. Capture timing + xid markers
	// so we can target them later. Each row's commit produces a
	// distinct xid; pg_xact_commit_timestamp() gives us the exact
	// commit time once track_commit_timestamp is on.
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "ALTER SYSTEM SET track_commit_timestamp = on"); err != nil {
		t.Fatalf("track_commit_timestamp: %v", err)
	}
	pgC.Stop(ctx, nil)
	pgC.Start(ctx)
	time.Sleep(3 * time.Second)

	for _, s := range []string{
		"CREATE TABLE smoke (id int PRIMARY KEY)",
		"INSERT INTO smoke VALUES (1)",
		"CHECKPOINT",
	} {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, s); err != nil {
			t.Fatalf("seed pre-mark %q: %v", s, err)
		}
	}
	// Capture the xid that committed row 1 — recovery_target_xid stops
	// AFTER the named xid commits, so target=xid_of_row1 means rows 1
	// (and earlier) are visible, row 2 is not.
	xidStr, err := dockerExecPSQLQuery(ctx, srcID, pgPwd,
		"SELECT xmin::text FROM smoke WHERE id = 1")
	if err != nil {
		t.Fatalf("capture xid: %v", err)
	}
	row1XID := strings.TrimSpace(xidStr)
	if row1XID == "" {
		t.Fatal("row1 xid empty")
	}

	// Capture commit time of row 1 for target_time.
	timeStr, err := dockerExecPSQLQuery(ctx, srcID, pgPwd,
		"SELECT to_char(pg_xact_commit_timestamp(xmin), 'YYYY-MM-DD HH24:MI:SS.US') FROM smoke WHERE id = 1")
	if err != nil {
		t.Fatalf("capture commit_ts: %v", err)
	}
	row1CommitTS := strings.TrimSpace(timeStr)

	// Sleep then add row 2 and 3 so target_time can land between
	// row1's commit and row2's. 1 second is more than enough resolution.
	time.Sleep(1500 * time.Millisecond)

	for _, s := range []string{
		"INSERT INTO smoke VALUES (2)",
		"INSERT INTO smoke VALUES (3)",
		"SELECT pg_switch_wal()",
		"CHECKPOINT",
	} {
		if err := dockerExecPSQL(ctx, srcID, pgPwd, s); err != nil {
			t.Fatalf("seed post-mark %q: %v", s, err)
		}
	}
	time.Sleep(2 * time.Second)
	if err := adapter.RefreshWALArchive(ctx, src); err != nil {
		t.Fatalf("RefreshWALArchive: %v", err)
	}

	// Helper to run one variant and tear down its container.
	runVariant := func(t *testing.T, label string, req domain.RestoreRequest) {
		t.Helper()
		req.NewProjectID = "pitr-variant-" + label
		req.BackupID = ref.ID
		resp, err := adapter.Restore(ctx, src, req)
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		t.Cleanup(func() {
			_ = rd.StopContainer(context.Background(), resp.Namespace)
			_ = rd.RemoveContainer(context.Background(), resp.Namespace)
		})
		deadline := time.Now().Add(90 * time.Second)
		var lastOut string
		for time.Now().Before(deadline) {
			got, err := dockerExecPSQLQuery(ctx, resp.Namespace, src.Password, "SELECT id FROM smoke ORDER BY id")
			if err == nil {
				lastOut = strings.TrimSpace(got)
				if lastOut == "1" {
					return
				}
			}
			time.Sleep(2 * time.Second)
		}
		logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "40", resp.Namespace).CombinedOutput()
		t.Logf("=== %s container logs ===\n%s", label, string(logs))
		t.Fatalf("PITR (%s) did not stop at target. Want rows=[1], got=%q", label, lastOut)
	}

	t.Run("TargetXID", func(t *testing.T) {
		runVariant(t, "xid", domain.RestoreRequest{TargetXID: row1XID})
	})
	t.Run("TargetTime", func(t *testing.T) {
		// Pick a time strictly after row1 commit, strictly before
		// row2 commit. Add 500ms to row1's commit_ts.
		base, err := time.Parse("2006-01-02 15:04:05.000000", row1CommitTS)
		if err != nil {
			t.Fatalf("parse commit_ts %q: %v", row1CommitTS, err)
		}
		target := base.Add(500 * time.Millisecond)
		runVariant(t, "time", domain.RestoreRequest{TargetTime: &domain.FlexTime{Time: target}})
	})
}

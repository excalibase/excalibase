//go:build integration

package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
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
//  1. Seed source postgres with `CREATE TABLE smoke; INSERT 4242`
//  2. DockerBackupAdapter.TriggerManual → S3 (LocalStack)
//  3. DockerBackupAdapter.Restore — creates a fresh container,
//     gunzips + CopyToContainer the tar into /var/lib/postgresql/data,
//     starts it, waits healthy
//  4. Query the restored container: `SELECT n FROM smoke` must
//     return 4242 (proves the data is the source's, not initdb's)
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

	// --- LocalStack S3 + real docker adapter wiring -----------------------
	bucket := "excalibase-restore-e2e"
	uploader := newLocalStackUploader(ctx, t, bucket)
	adapter, store, vault := newRestoreAdapter(ctx, t, uploader, bucket)

	src := &domain.DatabaseInstance{
		ProjectID:       "src-restore",
		OrgID:           "org",
		Namespace:       srcID,
		DatabaseName:    "app",
		Username:        defaultPostgresSuperuser,
		Password:        pgPwd,
		PostgresVersion: "16-alpine",
		DeploymentMode:  domain.ModeDocker,
		Status:          "ACTIVE",
	}
	store.Create(src)

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
	resp, err := adapter.Restore(ctx, src, domain.RestoreRequest{NewProjectName: "restored-001", TargetProjectID: "restored-001"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup of the restored container.
		_ = cleanupContainer(resp.Namespace)
	})
	t.Logf("restore returned: project=%s container=%s host=%s", resp.ProjectID, resp.Namespace, resp.Host)

	// --- Verify restored DB has the source's data ------------------------
	// Wait for postgres to finish recovery + accept queries. WaitForHealthy
	// returns on container "running" but the postgres image has no
	// HEALTHCHECK — pg may still be replaying WAL.
	waitForQueryResult(ctx, t, resp.Namespace, src.Password,
		"SELECT n FROM smoke", "4242", 60*time.Second)

	// --- Verify the restored project is usable through the platform ------
	// The row must exist (the API resolves projects through it) and the
	// vault must hold excalibase_app credentials that the restored database
	// actually accepts — the two halves of EXC-366.
	registered, err := store.FindByProjectID("restored-001")
	if err != nil || registered == nil {
		t.Fatalf("restored project was not registered in the platform store: %v", err)
	}
	if registered.Status != "ACTIVE" || registered.RestoredFromProjectID != src.ProjectID {
		t.Errorf("registered row: %+v", registered)
	}
	appCreds, err := vault.Get(vaultCredentialPath("restored-001", roleApp))
	if err != nil {
		t.Fatalf("excalibase_app credentials missing from vault: %v", err)
	}
	assertRoleCanConnect(ctx, t, resp.Namespace, appCreds)
}

// assertRoleCanConnect proves the password the platform filed in vault is the
// one the restored database accepts — the reset actually reached the role.
func assertRoleCanConnect(ctx context.Context, t *testing.T, containerID string, creds map[string]string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "exec",
		"-e", "PGPASSWORD="+creds["password"], containerID,
		"psql", "-h", "127.0.0.1", "-U", creds["username"], "-d", creds["database"], "-tAc", "SELECT 1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vault credentials rejected by the restored database: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Errorf("unexpected query output: %q", string(out))
	}
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

//go:build integration

package service

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// TestCopyNow_HasWhatWasWrittenAfterTheLastBackup is EXC-667's "now" proved
// on a real database: a row written after the last backup is in the copy,
// because the copy backs the source up first and restores from that backup.
// The copy is a registered project of its own whose credentials open it, and
// nothing the source issued opens the copy.
func TestCopyNow_HasWhatWasWrittenAfterTheLastBackup(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	pgPwd := testutil.FixturePassword("pg-copy-now-int")
	pgC, err := tcpg.Run(ctx, "postgres:16-alpine",
		tcpg.WithDatabase("app"), tcpg.WithUsername("postgres"), tcpg.WithPassword(pgPwd),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start source postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })
	srcID := pgC.GetContainerID()

	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CREATE TABLE marks (n int); INSERT INTO marks VALUES (1);"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedProjectOwner(ctx, t, srcID, pgPwd)
	seedSourceEndUserAuth(ctx, t, srcID, pgPwd)

	bucket := "excalibase-copy-now-e2e"
	uploader := newLocalStackUploader(ctx, t, bucket)
	adapter, store, vault, _ := newRestoreAdapterWithProbe(ctx, t, uploader, bucket)
	src := &domain.DatabaseInstance{
		ProjectID: "src-copy", OrgID: "org", Tier: domain.Standard, StorageSize: "50Gi",
		Namespace: srcID, DatabaseName: "app", Username: projectOwnerFixture, Password: pgPwd,
		PostgresVersion: "16", DeploymentMode: domain.ModeDocker, Status: string(domain.StatusActive),
		BackupEnabled: boolPtr(true),
	}
	if err := store.Create(src); err != nil {
		t.Fatal(err)
	}
	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{domain.ModeDocker: adapter}, "")
	svc.SetOrgProjectCapacity(unlimitedCapacity{})

	// The last backup holds only the first row.
	earlier, err := adapter.TriggerManual(ctx, src)
	if err != nil || earlier.Status != "COMPLETED" {
		t.Fatalf("earlier backup: %+v %v", earlier, err)
	}
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "INSERT INTO marks VALUES (2);"); err != nil {
		t.Fatalf("write after the backup: %v", err)
	}

	req := domain.RestoreRequest{NewProjectName: "copy-now", TargetProjectID: "copy-now", CopyNow: true, Copy: true}
	if err := svc.CheckCopy(src, req); err != nil {
		t.Fatalf("CheckCopy: %v", err)
	}
	if err := svc.RestoreSubmittedCopy(ctx, src.ProjectID, req); err != nil {
		t.Fatalf("copy now: %v", err)
	}
	copied, err := store.FindByProjectID("copy-now")
	if err != nil || copied == nil {
		t.Fatalf("the copy was not registered: %v", err)
	}
	t.Cleanup(func() { _ = cleanupContainer(copied.Namespace) })

	waitForQueryResult(ctx, t, copied.Namespace, src.Password, "SELECT string_agg(n::text, ',' ORDER BY n) FROM marks", "1,2", 90*time.Second)
	if copied.Status != string(domain.StatusActive) || copied.RestoredFromProjectID != src.ProjectID {
		t.Errorf("copy row: %+v", copied)
	}
	if copied.RestoredFromBackupID == "" || copied.RestoredFromBackupID == earlier.ID {
		t.Errorf("the copy must come from the backup taken for it, got %q (earlier %q)", copied.RestoredFromBackupID, earlier.ID)
	}
	appCreds, err := vault.Get(vaultCredentialPath("copy-now", roleApp))
	if err != nil {
		t.Fatalf("the copy's excalibase_app credentials are missing: %v", err)
	}
	assertRoleCanConnect(ctx, t, copied.Namespace, appCreds)
	assertNothingTheSourceIssuedOpensTheCopy(ctx, t, copied.Namespace, srcID, src.Password)
}

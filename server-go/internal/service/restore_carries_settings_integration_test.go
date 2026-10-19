//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// EXC-639 on a real single-host restore registering into a real platform
// database: the copy answers signed-in queries the way the source does,
// because it carries the source's API permissions, tracked functions, CORS
// origins and auth settings.
//
// Run: go test -tags=integration ./internal/service/ -run TestSingleHostRestoredCopyCarries -v
func TestSingleHostRestoredCopyCarriesTheSourcesAPISettings(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	pgPwd := testutil.FixturePassword("pg-restore-settings")
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
	if err := dockerExecPSQL(ctx, srcID, pgPwd, "CREATE TABLE workspaces (id int, name text); INSERT INTO workspaces VALUES (1, 'w');"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedProjectOwner(ctx, t, srcID, pgPwd)
	_ = dockerExecPSQL(ctx, srcID, pgPwd, "CHECKPOINT;")

	platform := startPlatformPostgres(ctx, t)
	bucket := "excalibase-restore-settings"
	adapter, _ := newRestoreAdapterOn(t, newLocalStackUploader(ctx, t, bucket), bucket, platform, platform)

	src := &domain.DatabaseInstance{
		ProjectID: "src-settings", OrgID: "org", Tier: domain.Standard, StorageSize: "50Gi",
		Namespace: srcID, DatabaseName: "app", Username: projectOwnerFixture, Password: pgPwd,
		PostgresVersion: "16", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	}
	if err := platform.Create(src); err != nil {
		t.Fatalf("register source: %v", err)
	}
	seedSourceAPISettings(ctx, t, platform, src.ProjectID)

	if _, err := adapter.TriggerManual(ctx, src); err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	resp, err := adapter.Restore(ctx, src, domain.RestoreRequest{NewProjectName: "settings-copy", TargetProjectID: "settings-copy"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Cleanup(func() { _ = cleanupContainer(resp.Namespace) })

	assertCopyCarriesTheSourcesAPISettings(ctx, t, platform, src.ProjectID, "settings-copy")
}

// seedSourceAPISettings is what the prod test's source had: a signed-in role
// allowed to read a table, plus the rest of the project's API settings.
func seedSourceAPISettings(ctx context.Context, t *testing.T, platform *pgstore.Store, projectID string) {
	t.Helper()
	perms := platform.Permissions()
	if _, err := perms.PutPermission(ctx, domain.TablePermission{ProjectID: projectID, Table: "public.workspaces",
		Role: "authenticated", Operation: domain.PermissionSelect, Definition: json.RawMessage(`{"columns":["id","name"],"filter":{}}`)}); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if err := perms.TrackFunction(ctx, domain.TrackedFunction{ProjectID: projectID, Function: "public.search", ExposedAs: "QUERY"}); err != nil {
		t.Fatalf("track function: %v", err)
	}
	if err := perms.PutFunctionPermission(ctx, projectID, "public.search", "authenticated"); err != nil {
		t.Fatalf("function permission: %v", err)
	}
	if err := platform.SetCorsOrigins(ctx, projectID, []string{"https://app.example.com"}); err != nil {
		t.Fatalf("cors: %v", err)
	}
	if err := platform.SetAuthSettings(ctx, projectID, domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://app.example.com"}); err != nil {
		t.Fatalf("auth settings: %v", err)
	}
}

func assertCopyCarriesTheSourcesAPISettings(ctx context.Context, t *testing.T, platform *pgstore.Store, sourceID, copyID string) {
	t.Helper()
	row, err := platform.FindByProjectID(copyID)
	if err != nil || row == nil || row.Status != "ACTIVE" {
		t.Fatalf("copy row = %+v (%v), want ACTIVE", row, err)
	}
	source, err := platform.Permissions().Document(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := platform.Permissions().Document(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Version == 0 || !reflect.DeepEqual(copied.Tables, source.Tables) ||
		!reflect.DeepEqual(copied.FunctionPermissions, source.FunctionPermissions) || len(copied.Functions) != len(source.Functions) {
		t.Errorf("copy permissions = %+v, want the source's %+v", copied, source)
	}
	if origins, _ := platform.GetCorsOrigins(ctx, copyID); !reflect.DeepEqual(origins, []string{"https://app.example.com"}) {
		t.Errorf("copy CORS origins = %v", origins)
	}
	if auth, ok, _ := platform.GetAuthSettings(ctx, copyID); !ok || !auth.RequireEmailVerification {
		t.Errorf("copy auth settings = %+v (%v), want the source's", auth, ok)
	}
}

// The same on a single host's MySQL restore.
//
// Run: go test -tags=integration ./internal/service/ -run TestHostMySQLRestoredCopyCarries -v
func TestHostMySQLRestoredCopyCarriesTheSourcesAPISettings(t *testing.T) {
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	suffix := time.Now().UnixNano() % 1_000_000
	source, copyID := fmt.Sprintf("it-mps-%d", suffix), fmt.Sprintf("it-mpc-%d", suffix)
	mysqlProv := provisioner.NewDockerMySQLProvisioner(docker)
	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	result, err := mysqlProv.Provision(ctx, domain.ProvisioningRequest{ProjectName: source, DBType: domain.MySQL, DatabaseName: "shop"}, plan, func(domain.ProvisioningStage) {})
	for _, project := range []string{source, copyID} {
		t.Cleanup(func() { _ = mysqlProv.RemoveData(context.Background(), project) })
	}
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := provisioner.MySQLPasswords{Admin: "Adm1n-" + source, App: "app-" + source, AuthAdmin: "auth-" + source, Watcher: "cdc-" + source}
	if err := mysqlProv.EnsurePlatformUsers(ctx, result.Namespace, source, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	platform := startPlatformPostgres(ctx, t)
	inst := &domain.DatabaseInstance{ProjectID: source, OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker,
		Tier: domain.Standard, Namespace: result.Namespace, Host: result.Host, DatabaseName: "shop", MySQLVersion: config.DefaultMySQLVersion(), Status: "ACTIVE"}
	if err := platform.Create(inst); err != nil {
		t.Fatal(err)
	}
	seedSourceAPISettings(ctx, t, platform, source)

	registrar := NewProvisioningService(platform, provisioner.NewFactory(mysqlProv), nil)
	registrar.SetOrgStore(testOrgs())
	setOrgTier(registrar, "org1", domain.Standard)
	registrar.SetVault(newFakeVault())
	registrar.SetRestoredSettings(platform)
	adapter := NewHostMySQLBackupAdapter(HostMySQLBackupAdapterConfig{
		Docker: docker, MySQL: mysqlProv, Streamer: NewDockerStreamer(docker.RawClient()), Uploader: newFakeS3Uploader(),
		Records: &fakeBackupRecordStore{}, Bucket: "it", KeyPrefix: "backups/", Instances: platform,
	})
	adapter.SetProjectRegistrar(registrar)
	adapter.SetDatabaseProbe(alwaysAnswers{})
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Standard, Config: plan}})

	if _, err := adapter.TriggerManual(ctx, inst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := adapter.Restore(ctx, inst, domain.RestoreRequest{NewProjectName: copyID, TargetProjectID: copyID}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertCopyCarriesTheSourcesAPISettings(ctx, t, platform, source, copyID)
}

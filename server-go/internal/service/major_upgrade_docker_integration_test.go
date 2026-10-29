//go:build integration

package service

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-668 on a real engine: a single host's Postgres 16 project moves to 17
// with its rows, roles, owner login, extensions and WAL archiving; a load
// that fails puts 16 back as it was.

// brokenDump replaces the dump the new container receives, so its load fails.
type brokenDump struct{ *provisioner.RealDockerClient }

func (b brokenDump) CopyToContainer(ctx context.Context, id, dst string, content io.Reader) error {
	if dst != "/tmp" {
		return b.RealDockerClient.CopyToContainer(ctx, id, dst, content)
	}
	_, _ = io.Copy(io.Discard, content)
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	body := []byte("CREATE TABLE broken (;\n")
	_ = writer.WriteHeader(&tar.Header{Name: "excalibase-upgrade/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = writer.WriteHeader(&tar.Header{Name: "excalibase-upgrade/globals.sql", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	_, _ = writer.Write(body)
	_ = writer.Close()
	return b.RealDockerClient.CopyToContainer(ctx, id, dst, &buf)
}

type liveUpgrade struct {
	docker   *provisioner.RealDockerClient
	adapter  *DockerBackupAdapter
	store    *storage.FileSystemStore
	runs     *memMajorUpgrades
	uploader *AWSS3Uploader
	result   *provisioner.ProvisioningResult
	project  string
	bucket   string
}

func newLiveUpgrade(ctx context.Context, t *testing.T) *liveUpgrade {
	t.Helper()
	config.PublishPostgresCatalogForTest()
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{Host: os.Getenv("DOCKER_HOST")})
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	t.Cleanup(func() { docker.Close() })
	project := fmt.Sprintf("exc668-%d", time.Now().UnixNano()%100000)
	result, err := provisioner.NewDockerPostgreSQLProvisioner(docker).Provision(ctx, domain.ProvisioningRequest{
		ProjectName: project, DBType: domain.PostgreSQL, PostgresVersion: "16",
		Backup: &domain.BackupSettings{Enabled: true},
	}, config.TierConfig{Memory: "512Mi", CPU: "0.5"}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() {
		for _, name := range []string{provisioner.DatabaseContainerName(project), provisioner.PreUpgradeContainerName(project, "mu-live"),
			provisioner.DatabaseContainerName(project) + "-extensions-mu-live"} {
			_ = docker.RemoveContainer(context.Background(), name)
		}
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	seedUpgradeSource(ctx, t, docker, result)

	bucket := "excalibase-upgrade-e2e"
	uploader := newLocalStackUploader(ctx, t, bucket)
	store, _ := storage.NewFileSystemStore(t.TempDir())
	on := true
	_ = store.Create(&domain.DatabaseInstance{
		ProjectID: project, OrgID: "org", Namespace: result.Namespace, DatabaseName: result.DatabaseName,
		Username: result.Username, Password: result.Password, PostgresVersion: "16", Tier: domain.Free,
		DeploymentMode: domain.ModeDocker, Status: string(domain.StatusUpgrading), BackupEnabled: &on, DBType: domain.PostgreSQL,
	})
	sdk, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdk.Close() })
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner: NewDockerBackupRunner(sdk), Uploader: uploader, Records: &fakeBackupRecordStore{},
		Bucket: bucket, KeyPrefix: "backups/", Instances: store,
	})
	adapter.SetDockerClient(docker)
	free, _ := config.GetTierConfig(domain.Free)
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: free}})
	runs := newMemMajorUpgrades(store)
	adapter.SetMajorUpgradeArchives(NewMajorUpgradeArchives(runs))
	return &liveUpgrade{docker: docker, adapter: adapter, store: store, runs: runs, uploader: uploader, result: result, project: project, bucket: bucket}
}

// seedUpgradeSource gives the database the project's roles, an extension and rows.
func seedUpgradeSource(ctx context.Context, t *testing.T, docker *provisioner.RealDockerClient, result *provisioner.ProvisioningResult) {
	t.Helper()
	spec := projectRoleSpec{databaseName: result.DatabaseName, adminUsername: result.Username}
	creds := projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: "App-Password-1", watcherPassword: "Watcher-Password-1", schedulerPassword: "Scheduler-Password-1"}
	superuser := []string{"psql", "-U", "postgres", "-d", result.DatabaseName, "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"}
	if _, err := docker.ExecInContainerStdin(ctx, result.Namespace, superuser, projectRoleSQL(spec, creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("register roles: %v", err)
	}
	seed := "CREATE EXTENSION pgcrypto;\nSET ROLE excalibase_app;\n" +
		"CREATE TABLE public.orders (id serial PRIMARY KEY, item text, digest text);\n" +
		"INSERT INTO public.orders (item, digest) SELECT 'item ' || n, encode(digest('x' || n, 'sha256'), 'hex') FROM generate_series(1, 500) n;\n" +
		// A tenant's own check runs while the dump loads; it must run as the
		// tenant, never as the superuser the load connects as.
		"CREATE FUNCTION public.not_as_superuser(int) RETURNS boolean LANGUAGE plpgsql AS $$\n" +
		"BEGIN IF current_setting('is_superuser') = 'on' THEN RAISE EXCEPTION 'tenant code ran as superuser'; END IF; RETURN true; END $$;\n" +
		"CREATE TABLE public.guarded (id int CHECK (public.not_as_superuser(id)));\n" +
		"INSERT INTO public.guarded SELECT generate_series(1, 3);\n"
	if _, err := docker.ExecInContainerStdin(ctx, result.Namespace, superuser, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func (l *liveUpgrade) upgrader() *DockerMajorUpgrader {
	return NewDockerMajorUpgrader(DockerMajorUpgraderConfig{Backups: l.adapter,
		Timings: DockerUpgradeTimings{Poll: time.Second, Load: 5 * time.Minute, Start: time.Minute}})
}

func (l *liveUpgrade) start(ctx context.Context, t *testing.T) (*domain.DatabaseInstance, *domain.MajorUpgrade) {
	t.Helper()
	inst, _ := l.store.FindByProjectID(l.project)
	run := &domain.MajorUpgrade{ID: "mu-live", ProjectID: l.project, Engine: "postgresql", FromVersion: "16", ToVersion: "17", Step: domain.MajorUpgradeStepBackup}
	if err := l.runs.StartMajorUpgrade(ctx, run); err != nil {
		t.Fatal(err)
	}
	return inst, run
}

type liveProgress struct {
	ctx  context.Context
	runs *memMajorUpgrades
	run  *domain.MajorUpgrade
}

func (p *liveProgress) Step(string)             {}
func (p *liveProgress) WritesStopped(time.Time) {}
func (p *liveProgress) Converted(at time.Time) error {
	return p.runs.MarkMajorUpgradeConverted(p.ctx, p.run.ID, p.run.ProjectID, p.run.ToVersion, at)
}

func (l *liveUpgrade) login(ctx context.Context, t *testing.T, namespace string) *sql.DB {
	t.Helper()
	login := *l.result
	login.Namespace = namespace
	return connectSingleHost(ctx, t, l.docker, &login)
}

func TestSingleHostMajorUpgradeMovesTheDatabaseTo17(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	live := newLiveUpgrade(ctx, t)
	upgrader := live.upgrader()
	inst, run := live.start(ctx, t)

	if blockers, err := upgrader.Preflight(ctx, inst, "17"); err != nil || len(blockers) != 0 {
		t.Fatalf("preflight: %v %v", blockers, err)
	}
	if _, err := upgrader.TakeBackup(ctx, inst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	outcome := upgrader.Convert(ctx, inst, *run, &liveProgress{ctx: ctx, runs: live.runs, run: run})
	if !outcome.Converted {
		logs, _ := live.docker.ContainerState(ctx, provisioner.DatabaseContainerName(live.project))
		t.Fatalf("outcome %+v (new container %+v)", outcome, logs)
	}
	if err := upgrader.WaitServing(ctx, inst); err != nil {
		t.Fatalf("wait serving: %v", err)
	}
	if warnings := upgrader.AfterSteps(ctx, inst); len(warnings) != 0 {
		t.Fatalf("after steps: %v", warnings)
	}

	row, _ := live.store.FindByProjectID(live.project)
	if row.PostgresVersion != "17" || row.Namespace == live.result.Namespace {
		t.Fatalf("project row: version %s container %s", row.PostgresVersion, row.Namespace)
	}
	app := live.login(ctx, t, row.Namespace)
	if version := scanString(t, app, "SELECT current_setting('server_version_num')"); !strings.HasPrefix(version, "17") {
		t.Fatalf("server version %s", version)
	}
	if got := scanString(t, app, "SELECT count(*)::text FROM public.orders"); got != "500" {
		t.Fatalf("rows after the upgrade: %s", got)
	}
	if got := scanString(t, app, "SELECT digest FROM public.orders WHERE id = 1"); got == "" {
		t.Fatal("the extension's data did not survive")
	}
	if !scanBool(t, app, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto')") {
		t.Fatal("pgcrypto is gone")
	}
	if got := scanString(t, app, "SELECT count(*)::text FROM public.guarded"); got != "3" {
		t.Fatalf("rows behind the tenant's check: %s", got)
	}
	if scanBool(t, app, "SELECT rolsuper FROM pg_roles WHERE rolname = current_user") {
		t.Fatal("the tenant's login became a superuser")
	}
	mustExec(t, app, "INSERT INTO public.orders (item) VALUES ('after')")

	superuser := *live.result
	superuser.Namespace, superuser.Username, superuser.Password = row.Namespace, "postgres", "any-password"
	if err := openSingleHost(ctx, t, live.docker, &superuser).PingContext(ctx); err == nil || !strings.Contains(err.Error(), "rejects connection") {
		t.Fatalf("the superuser is not rejected over the network: %v", err)
	}

	if err := live.adapter.RefreshWALArchive(ctx, row); err != nil {
		t.Fatalf("ship WAL from 17: %v", err)
	}
	shipped, err := live.uploader.List(ctx, live.bucket, "backups/"+live.project+"/pg17/wals/")
	if err != nil || len(shipped) == 0 {
		t.Fatalf("no WAL in the new archive: %v %v", shipped, err)
	}
	upgrader.Cleanup(ctx, row, *run)
	if state, _ := live.docker.ContainerState(ctx, provisioner.PreUpgradeContainerName(live.project, run.ID)); state.Found {
		t.Fatal("the old container was left behind")
	}
}

func TestSingleHostMajorUpgradePutsTheOldVersionBackWhenTheLoadFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	live := newLiveUpgrade(ctx, t)
	live.adapter.SetDockerClient(brokenDump{live.docker})
	upgrader := live.upgrader()
	inst, run := live.start(ctx, t)

	outcome := upgrader.Convert(ctx, inst, *run, &liveProgress{ctx: ctx, runs: live.runs, run: run})
	if !outcome.RolledBack {
		t.Fatalf("outcome %+v", outcome)
	}
	row, _ := live.store.FindByProjectID(live.project)
	if row.PostgresVersion != "16" || row.Namespace != live.result.Namespace {
		t.Fatalf("project row: version %s container %s", row.PostgresVersion, row.Namespace)
	}
	state, err := live.docker.ContainerState(ctx, provisioner.DatabaseContainerName(live.project))
	if err != nil || state.ID != live.result.Namespace || !state.Running {
		t.Fatalf("container under the project's name: %+v %v", state, err)
	}
	app := live.login(ctx, t, row.Namespace)
	if got := scanString(t, app, "SELECT count(*)::text FROM public.orders"); got != "500" {
		t.Fatalf("rows after the put back: %s", got)
	}
	if version := scanString(t, app, "SELECT current_setting('server_version_num')"); !strings.HasPrefix(version, "16") {
		t.Fatalf("server version %s", version)
	}
}

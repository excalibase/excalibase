package provisioner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func dockerMySQLRequest() domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: "p1", OrgID: "o1", DBType: domain.MySQL, DatabaseName: "shop"}
}

func newTestDockerMySQL(engine *fakeEngine) *DockerMySQLProvisioner {
	p := NewDockerMySQLProvisioner(engine)
	p.SetPollers(fastPoller(5), fastPoller(5))
	return p
}

func provisionDockerMySQL(t *testing.T, engine *fakeEngine) *ProvisioningResult {
	t.Helper()
	result, err := newTestDockerMySQL(engine).Provision(context.Background(), dockerMySQLRequest(), testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v (calls %v)", err, engine.calls)
	}
	return result
}

func TestDockerMySQLRunsTheCatalogueImageWithItsOwnStartScript(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	spec := engine.specs["excalibase-p1-mysql"]
	want, _ := config.MySQLImage(config.DefaultMySQLVersion())
	if spec.Image != want {
		t.Fatalf("image %q, want the catalogue's %q", spec.Image, want)
	}
	if spec.DataVolume != "/var/lib/mysql" {
		t.Fatalf("data volume %q", spec.DataVolume)
	}
	if len(spec.Entrypoint) != 2 || spec.Entrypoint[0] != "sh" || spec.Entrypoint[1] != "-c" || len(spec.Cmd) != 1 {
		t.Fatalf("entrypoint %v cmd %v", spec.Entrypoint, spec.Cmd)
	}
	if _, ok := spec.Ports["3306"]; !ok || len(spec.Ports) != 1 {
		t.Fatalf("ports %v, want 3306 alone", spec.Ports)
	}
	if spec.Limits.MemoryBytes != 1<<30 {
		t.Fatalf("memory %d, want the 1Gi MySQL floor", spec.Limits.MemoryBytes)
	}
	if result.Host != "excalibase-p1-mysql" || result.Namespace != "excalibase-p1-mysql" || result.Port != 3306 ||
		result.DatabaseName != "shop" || result.Username != MySQLUserAdmin || result.SSLMode != "REQUIRED" || result.Password != "" {
		t.Fatalf("result %+v", result)
	}
}

func TestDockerMySQLServerSettingsKeepBinlogsForPointInTimeAndRequireTLS(t *testing.T) {
	engine := newFakeEngine()
	provisionDockerMySQL(t, engine)
	script := engine.specs["excalibase-p1-mysql"].Cmd[0]
	for _, setting := range []string{
		"--gtid-mode=ON", "--enforce-gtid-consistency=ON", "--binlog-format=ROW", "--binlog-row-image=FULL",
		"--binlog-expire-logs-seconds=604800", "--max-allowed-packet=1073741824", "--require-secure-transport=ON",
		"--skip-name-resolve=ON", "--mysqlx=OFF", "--socket=/tmp/mysqld.sock", "--max-connections=100",
		"--innodb-buffer-pool-size=134217728", "--collation-server=utf8mb4_unicode_ci", "--log-bin=binlog",
	} {
		if !strings.Contains(script, setting) {
			t.Errorf("start script lacks %s:\n%s", setting, script)
		}
	}
	if !strings.Contains(script, "--initialize-insecure") || !strings.Contains(script, "exec mysqld") {
		t.Fatalf("start script must initialise an empty datadir and run mysqld:\n%s", script)
	}
}

func TestDockerMySQLLargePlanGivesSeventyPercentToTheBufferPool(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	if _, err := p.Provision(context.Background(), dockerMySQLRequest(), config.TierConfig{Memory: "4Gi", CPU: "2"}, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if script := engine.specs["excalibase-p1-mysql"].Cmd[0]; !strings.Contains(script, "--innodb-buffer-pool-size=3006477107") {
		t.Fatalf("buffer pool is not 70%% of 4Gi:\n%s", script)
	}
}

func TestDockerMySQLCreatesTheDatabaseOverTheSocketOnceAQueryAnswers(t *testing.T) {
	engine := newFakeEngine()
	looks := 0
	engine.execCode = func(_ string, cmd []string) int {
		looks++
		if looks < 3 {
			return 1
		}
		return 0
	}
	provisionDockerMySQL(t, engine)
	if len(engine.stdin) == 0 || !strings.Contains(engine.stdin[0], "CREATE DATABASE IF NOT EXISTS `shop`") {
		t.Fatalf("database not created: %v", engine.stdin)
	}
	if looks < 3 {
		t.Fatalf("created before a query answered (%d looks)", looks)
	}
}

func TestDockerMySQLRefusesWhatSingleHostMySQLDoesNotOffer(t *testing.T) {
	for name, change := range map[string]func(*domain.ProvisioningRequest){
		"documentdb":    func(r *domain.ProvisioningRequest) { r.DocumentDB = true },
		"database name": func(r *domain.ProvisioningRequest) { r.DatabaseName = "Shop-1" },
		"version":       func(r *domain.ProvisioningRequest) { r.MySQLVersion = "8.0.36" },
	} {
		t.Run(name, func(t *testing.T) {
			engine := newFakeEngine()
			req := dockerMySQLRequest()
			change(&req)
			if _, err := newTestDockerMySQL(engine).Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {}); err == nil {
				t.Fatal("want refusal")
			}
			if len(engine.containers) != 0 {
				t.Fatalf("a container was created: %v", engine.calls)
			}
		})
	}
}

func TestDockerMySQLFailsWhenTheServerNeverAnswers(t *testing.T) {
	engine := newFakeEngine()
	engine.execCode = func(string, []string) int { return 1 }
	_, err := newTestDockerMySQL(engine).Provision(context.Background(), dockerMySQLRequest(), testTier, func(domain.ProvisioningStage) {})
	if err == nil {
		t.Fatal("want failure when mysqld never answers")
	}
}

func TestDockerMySQLPauseStopsAndResumeWaitsForAQuery(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	ctx := context.Background()
	if err := p.Pause(ctx, result.Namespace, "p1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if stopped, err := p.WorkloadStopped(ctx, result.Namespace, "p1"); err != nil || !stopped {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
	engine.calls = nil
	if err := p.Resume(ctx, result.Namespace, "p1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if engine.indexOf("start:excalibase-p1-mysql") < 0 || engine.indexOf("exec:excalibase-p1-mysql:mysql") < 0 {
		t.Fatalf("resume must start and query: %v", engine.calls)
	}
	status, err := p.GetStatus(ctx, result.Namespace, "p1")
	if err != nil || !status.Ready {
		t.Fatalf("status %+v err %v", status, err)
	}
}

func TestDockerMySQLStatusIsNotReadyUntilAQueryAnswers(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	engine.execCode = func(string, []string) int { return 1 }
	status, err := newTestDockerMySQL(engine).GetStatus(context.Background(), result.Namespace, "p1")
	if err != nil || status.Ready {
		t.Fatalf("status %+v err %v", status, err)
	}
}

func TestDockerMySQLDeprovisionRemovesTheContainerAndIsIdempotent(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	for i := 0; i < 2; i++ {
		if err := p.Deprovision(context.Background(), result.Namespace, "p1"); err != nil {
			t.Fatalf("Deprovision %d: %v", i, err)
		}
	}
	if len(engine.containers) != 0 {
		t.Fatalf("containers left: %v", engine.containers)
	}
}

func TestDockerMySQLPlatformUsersGoOnStdinAndAFailureIsScrubbed(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	passwords := MySQLPasswords{Admin: "adm-secret", App: "app-secret", AuthAdmin: "auth-secret", Watcher: "cdc-secret"}
	if err := p.EnsurePlatformUsers(context.Background(), result.Namespace, "p1", "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	last := engine.stdin[len(engine.stdin)-1]
	if !strings.Contains(last, "CREATE USER IF NOT EXISTS 'admin'@'%' IDENTIFIED BY 'adm-secret' REQUIRE SSL") {
		t.Fatalf("users not created:\n%s", last)
	}
	for _, call := range engine.calls {
		if strings.Contains(call, "secret") {
			t.Fatalf("a password reached an exec argument: %s", call)
		}
	}
	engine.stdinAnswer = func(string, []string, string) (string, error) {
		return "", errors.New("exit code 1: ERROR 1396 at line 3: ALTER USER 'admin' IDENTIFIED BY 'new-secret'")
	}
	err := p.SetUserPassword(context.Background(), result.Namespace, "p1", MySQLUserAdmin, "new-secret")
	if err == nil || strings.Contains(err.Error(), "new-secret") {
		t.Fatalf("error %v must be reported without the password", err)
	}
}

func TestDockerMySQLSetsSeveralPasswordsInOneBatch(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	before := len(engine.stdin)
	if err := p.SetUserPasswords(context.Background(), result.Namespace, "p1", map[string]string{"admin": "a1", "excalibase_app": "b2"}); err != nil {
		t.Fatalf("SetUserPasswords: %v", err)
	}
	if len(engine.stdin) != before+1 {
		t.Fatalf("want one batch, got %d", len(engine.stdin)-before)
	}
	if batch := engine.stdin[before]; !strings.Contains(batch, "'admin'@'%' IDENTIFIED BY 'a1'") || !strings.Contains(batch, "'excalibase_app'@'%' IDENTIFIED BY 'b2'") {
		t.Fatalf("batch:\n%s", batch)
	}
}

func TestDockerMySQLClientWorkIsReadOverTheSocket(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	engine.stdinAnswer = func(_ string, cmd []string, stdin string) (string, error) {
		if !slices.Contains(cmd, "--skip-column-names") || !strings.Contains(stdin, "performance_schema") {
			return "", errors.New("unexpected query")
		}
		return "42\n", nil
	}
	work, err := p.ClientWork(context.Background(), result.Namespace, "p1")
	if err != nil || work != 42 {
		t.Fatalf("work %d err %v", work, err)
	}
	engine.stdinAnswer = func(string, []string, string) (string, error) { return "n/a\n", nil }
	if _, err := p.ClientWork(context.Background(), result.Namespace, "p1"); err == nil {
		t.Fatal("a reading that is not a number must fail")
	}
}

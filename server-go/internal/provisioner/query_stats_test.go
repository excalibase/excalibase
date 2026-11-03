package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func provisionedParameters(t *testing.T, queryStats bool) map[string]interface{} {
	t.Helper()
	mock := documentDBKube()
	p := NewPostgreSQLProvisioner(mock, "")
	p.SetQueryStats(queryStats)
	req := domain.ProvisioningRequest{ProjectName: "statproj", OrgID: "org1", DBType: domain.PostgreSQL, PostgresVersion: config.PostgresMajors()[0]}
	if _, err := p.Provision(context.Background(), req, config.TierConfig{Instances: 1, StorageSize: "5Gi"}, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	cluster := mock.CRDs["org1-statproj/statproj-postgres"]
	if cluster == nil {
		t.Fatal("no cluster was applied")
	}
	return cluster.Object["spec"].(map[string]interface{})["postgresql"].(map[string]interface{})["parameters"].(map[string]interface{})
}

// EXC-654: a cluster created with the feature on carries pg_stat_statements.
func TestProvisionWithQueryStatsTurnsOnStatementStatistics(t *testing.T) {
	if got := provisionedParameters(t, true)["pg_stat_statements.track"]; got != "top" {
		t.Errorf("pg_stat_statements.track = %v", got)
	}
	for name := range provisionedParameters(t, false) {
		if strings.HasPrefix(name, "pg_stat_statements") {
			t.Errorf("feature off rendered %s", name)
		}
	}
}

func TestMySQLQueryStatsGrantIsTheDigestTableOnlyToThePlatformLogin(t *testing.T) {
	want := "GRANT SELECT (`SCHEMA_NAME`, `DIGEST_TEXT`, `COUNT_STAR`, `SUM_TIMER_WAIT`, `SUM_ROWS_SENT`, `SUM_ROWS_AFFECTED`), DROP ON `performance_schema`.`events_statements_summary_by_digest` TO 'excalibase_app'@'%';"
	if MySQLQueryStatsGrantSQL != want {
		t.Errorf("grant = %s", MySQLQueryStatsGrantSQL)
	}
}

func TestMySQLGrantQueryStatsRunsTheGrantOnThePrimary(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	if err := prov.GrantQueryStats(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatal(err)
	}
	if len(mock.ExecStdin) != 1 || mock.ExecStdin[0] != MySQLQueryStatsGrantSQL {
		t.Fatalf("stdin %v", mock.ExecStdin)
	}
}

func TestDockerMySQLGrantQueryStatsRunsTheGrant(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	before := len(engine.stdin)
	if err := p.GrantQueryStats(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	if len(engine.stdin) != before+1 || engine.stdin[before] != MySQLQueryStatsGrantSQL {
		t.Fatalf("stdin %v", engine.stdin[before:])
	}
}

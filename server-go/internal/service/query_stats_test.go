package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// fakeStatsDatabases records the psql it is asked to run and answers with output.
type fakeStatsDatabases struct {
	output   string
	err      error
	commands [][]string
	enabled  []string
}

func (f *fakeStatsDatabases) primaryPsql(context.Context, *domain.DatabaseInstance) (roleSQLExecer, error) {
	return func(_ context.Context, cmd []string) (string, error) {
		f.commands = append(f.commands, cmd)
		return f.output, f.err
	}, nil
}

func (f *fakeStatsDatabases) EnableQueryStats(_ context.Context, projectID string) error {
	f.enabled = append(f.enabled, projectID)
	return f.err
}

// lastSQL is the script of the last psql run.
func (f *fakeStatsDatabases) lastSQL(t *testing.T) string {
	t.Helper()
	if len(f.commands) == 0 {
		t.Fatal("no psql ran")
	}
	cmd := f.commands[len(f.commands)-1]
	return cmd[len(cmd)-1]
}

type fakeStatsOpener struct {
	db  *sql.DB
	err error
}

func (f fakeStatsOpener) OpenMySQLStats(context.Context, string) (*sql.DB, error) { return f.db, f.err }

func statsProjects(insts ...*domain.DatabaseInstance) *fakestore.Instances {
	store := fakestore.NewInstances()
	for _, inst := range insts {
		store.Items[inst.ProjectID] = inst
	}
	return store
}

func pgStatsProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "p1", Namespace: "org-p1", DBType: domain.PostgreSQL, Status: "ACTIVE", DatabaseName: "appdb"}
}

func mysqlStatsProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "m1", Namespace: "org-m1", DBType: domain.MySQL, Status: "ACTIVE", DatabaseName: "shop"}
}

func TestQueryStats_PostgresReadsThePrimaryAsTheSuperuserInTheProjectDatabase(t *testing.T) {
	dbs := &fakeStatsDatabases{output: `[{"query":"SELECT $1","calls":3,"totalTimeMs":12.5,"meanTimeMs":4.1,"rows":3}]`}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	report, err := svc.List(context.Background(), "p1", "mean", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || report.Engine != "postgresql" || report.Sort != "mean" || len(report.Queries) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if q := report.Queries[0]; q.Query != "SELECT $1" || q.Calls != 3 || q.TotalTimeMs != 12.5 || q.MeanTimeMs != 4.1 || q.Rows != 3 {
		t.Errorf("query = %+v", q)
	}
	cmd := strings.Join(dbs.commands[0], " ")
	if !strings.Contains(cmd, "-U postgres") || !strings.Contains(cmd, "dbname='appdb'") {
		t.Errorf("psql = %s", cmd)
	}
	script := dbs.lastSQL(t)
	for _, want := range []string{"'mean'", "10)", "current_database()", "NOT r.rolsuper", "NOT r.rolreplication", "statement_timeout"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}

// Platform logins and Postgres' own roles are never the project's queries;
// excalibase_app is, because the API engine and Studio's SQL run as it.
func TestQueryStats_PostgresFilterNamesEveryPlatformRoleButTheAppRole(t *testing.T) {
	filter := projectStatementRoleFilter
	for _, name := range pgroles.ReservedNames() {
		if name == roleApp {
			continue
		}
		if !strings.Contains(filter, "'"+name+"'") {
			t.Errorf("filter does not name %s", name)
		}
	}
	for _, prefix := range pgroles.ReservedPrefixes() {
		if !strings.Contains(filter, "'"+prefix+"'") {
			t.Errorf("filter does not name prefix %s", prefix)
		}
	}
	if !strings.Contains(filter, "r.rolname = '"+roleApp+"'") {
		t.Error("excalibase_app is not kept")
	}
}

func TestQueryStats_PostgresWithoutTheExtensionIsReportedOff(t *testing.T) {
	dbs := &fakeStatsDatabases{output: "\n"}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	report, err := svc.List(context.Background(), "p1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Enabled || report.Queries == nil || len(report.Queries) != 0 || report.Sort != "total" {
		t.Errorf("report = %+v", report)
	}
}

func TestQueryStats_PostgresUnparseableOutputIsAnError(t *testing.T) {
	dbs := &fakeStatsDatabases{output: "garbage"}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 5); err == nil {
		t.Fatal("garbage was read as statistics")
	}
}

// A NOTICE on stderr ahead of the answer does not hide it.
func TestQueryStats_PostgresReadsTheAnswerAfterOtherOutput(t *testing.T) {
	dbs := &fakeStatsDatabases{output: "WARNING:  something\n[]\n"}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	report, err := svc.List(context.Background(), "p1", "total", 5)
	if err != nil || !report.Enabled {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
}

func TestQueryStats_RefusesAnUnknownSort(t *testing.T) {
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), &fakeStatsDatabases{}, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "calls; DROP TABLE x", 5); !errors.Is(err, ErrQueryStatsSort) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryStats_LimitIsBounded(t *testing.T) {
	for limit, want := range map[int]string{0: "50)", -3: "50)", 7: "7)", 5000: "100)"} {
		dbs := &fakeStatsDatabases{output: "[]"}
		svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
		if _, err := svc.List(context.Background(), "p1", "total", limit); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dbs.lastSQL(t), "excalibase_query_stats('total', "+want) {
			t.Errorf("limit %d: script %q", limit, dbs.lastSQL(t))
		}
	}
}

func TestQueryStats_RefusesAProjectThatIsNotRunning(t *testing.T) {
	paused := pgStatsProject()
	paused.Status = string(domain.StatusPaused)
	svc := NewQueryStatsService(statsProjects(paused), &fakeStatsDatabases{}, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 5); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("err = %v", err)
	}
	if err := svc.Reset(context.Background(), "p1"); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("reset err = %v", err)
	}
}

func TestQueryStats_PostgresResetClearsOnlyTheProjectsRolesInItsDatabase(t *testing.T) {
	dbs := &fakeStatsDatabases{output: "reset\n"}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	if err := svc.Reset(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	script := dbs.lastSQL(t)
	if !strings.Contains(script, "pg_stat_statements_reset") || !strings.Contains(script, projectStatementRoleFilter) ||
		!strings.Contains(script, "current_database()") {
		t.Errorf("reset script = %s", script)
	}
}

func TestQueryStats_PostgresResetWithoutTheExtensionIsRefused(t *testing.T) {
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), &fakeStatsDatabases{output: ""}, nil, features.NewStatic())
	if err := svc.Reset(context.Background(), "p1"); !errors.Is(err, ErrQueryStatsOff) {
		t.Fatalf("err = %v", err)
	}
}

// MySQL projects are served only while the mysql feature is on.
func TestQueryStats_MySQLNeedsTheMySQLFeature(t *testing.T) {
	svc := NewQueryStatsService(statsProjects(mysqlStatsProject()), &fakeStatsDatabases{}, fakeStatsOpener{}, features.NewStatic())
	if _, err := svc.List(context.Background(), "m1", "total", 5); !errors.Is(err, ErrNotForMySQL) {
		t.Fatalf("list err = %v", err)
	}
	if err := svc.Reset(context.Background(), "m1"); !errors.Is(err, ErrNotForMySQL) {
		t.Fatalf("reset err = %v", err)
	}
	if err := svc.Enable(context.Background(), "m1"); !errors.Is(err, ErrNotForMySQL) {
		t.Fatalf("enable err = %v", err)
	}
}

func mysqlStatsService(t *testing.T) (*QueryStatsService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewQueryStatsService(statsProjects(mysqlStatsProject()), &fakeStatsDatabases{}, fakeStatsOpener{db: db},
		features.NewStatic(features.MySQL)), mock
}

func TestQueryStats_MySQLReadsTheDigestsOfTheProjectDatabaseOnly(t *testing.T) {
	svc, mock := mysqlStatsService(t)
	mock.ExpectQuery(`FROM performance_schema\.events_statements_summary_by_digest\s+WHERE SCHEMA_NAME = \?.*ORDER BY SUM_TIMER_WAIT / COUNT_STAR DESC LIMIT \?`).
		WithArgs("shop", 20).
		WillReturnRows(sqlmock.NewRows([]string{"DIGEST_TEXT", "COUNT_STAR", "SUM_TIMER_WAIT", "ROWS"}).
			AddRow("SELECT * FROM `orders` WHERE `id` = ?", 4, uint64(8_000_000_000), 4))
	report, err := svc.List(context.Background(), "m1", "mean", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || report.Engine != "mysql" || len(report.Queries) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if q := report.Queries[0]; q.Calls != 4 || q.TotalTimeMs != 8 || q.MeanTimeMs != 2 || q.Rows != 4 {
		t.Errorf("query = %+v", q)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The platform login has not been granted the digests: statistics are off.
func TestQueryStats_MySQLWithoutTheGrantIsReportedOff(t *testing.T) {
	svc, mock := mysqlStatsService(t)
	mock.ExpectQuery("events_statements_summary_by_digest").
		WillReturnError(&mysql.MySQLError{Number: 1142, Message: "SELECT command denied"})
	report, err := svc.List(context.Background(), "m1", "total", 5)
	if err != nil {
		t.Fatal(err)
	}
	if report.Enabled || len(report.Queries) != 0 {
		t.Errorf("report = %+v", report)
	}
}

func TestQueryStats_MySQLResetTruncatesTheDigests(t *testing.T) {
	svc, mock := mysqlStatsService(t)
	mock.ExpectExec(`TRUNCATE TABLE performance_schema\.events_statements_summary_by_digest`).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := svc.Reset(context.Background(), "m1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestQueryStats_EnableIsTheProvisioningServicesJob(t *testing.T) {
	dbs := &fakeStatsDatabases{}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	if err := svc.Enable(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if len(dbs.enabled) != 1 || dbs.enabled[0] != "p1" {
		t.Errorf("enabled = %v", dbs.enabled)
	}
}

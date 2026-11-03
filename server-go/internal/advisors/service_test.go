package advisors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakePools struct {
	postgres, mysql *sql.DB
	err             error
}

func (p fakePools) Open(context.Context, string) (*sql.DB, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.postgres == nil {
		return nil, fmt.Errorf("%w: p1", domain.ErrPostgresOnly)
	}
	return p.postgres, nil
}

func (p fakePools) OpenMySQL(_ context.Context, _ string, script bool) (*sql.DB, error) {
	if script {
		return nil, errors.New("advisors asked for a script pool")
	}
	return p.mysql, nil
}

type fakePermissions struct {
	doc *domain.PermissionDocument
	err error
}

func (f fakePermissions) Document(context.Context, string) (*domain.PermissionDocument, error) {
	return f.doc, f.err
}

var anonEverything = &domain.PermissionDocument{Tables: []domain.TablePermissions{
	{Table: "app.orders", Role: "anon", Select: raw(`{"filter":{},"columns":"*"}`)},
}}

func mockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func statusOf(report *Report, check string) CheckStatus {
	for _, status := range report.Checks {
		if status.ID == check {
			return status
		}
	}
	return CheckStatus{}
}

// The MySQL admin login is not granted performance_schema: the checks that
// need it say so instead of reporting a clean result.
func TestMySQLWithoutPerformanceSchemaGrantReportsUnavailable(t *testing.T) {
	db, mock := mockDB(t)
	denied := &mysql.MySQLError{Number: 1142, Message: "SELECT command denied"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DATABASE\(\)`).WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow("app"))
	mock.ExpectQuery(`PRIMARY KEY`).WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectQuery(`information_schema.statistics`).WillReturnRows(sqlmock.NewRows([]string{"t", "i", "n", "c", "p", "ty"}))
	mock.ExpectQuery(`referenced_table_name`).WillReturnRows(sqlmock.NewRows([]string{"t", "c", "col"}))
	mock.ExpectQuery(`@@performance_schema`).WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(1))
	mock.ExpectQuery(`table_io_waits_summary_by_index_usage`).WillReturnError(denied)
	mock.ExpectRollback()

	svc := NewService(fakePools{mysql: db}, fakePermissions{doc: anonEverything}, Options{})
	report, err := svc.Run(context.Background(), "p1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Engine != EngineMySQL {
		t.Fatalf("engine = %s", report.Engine)
	}
	for _, check := range []string{CheckUnusedIndex, CheckLargeTableNoIndexUse} {
		if status := statusOf(report, check); status.Status != StatusUnavailable || status.Reason == "" {
			t.Errorf("%s: %+v", check, status)
		}
	}
	if status := statusOf(report, CheckFKWithoutIndex); status.Status != StatusNotApplicable {
		t.Errorf("fk: %+v", status)
	}
	for _, check := range []string{CheckNoPrimaryKey, CheckDuplicateIndex, CheckAnonUnfiltered, CheckWriteWithoutSelect} {
		if status := statusOf(report, check); status.Status != StatusRan {
			t.Errorf("%s: %+v", check, status)
		}
	}
	if len(onlyCheck(report.Findings, CheckAnonUnfiltered)) != 1 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLWithPerformanceSchemaOffReportsUnavailable(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DATABASE\(\)`).WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow("app"))
	mock.ExpectQuery(`PRIMARY KEY`).WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectQuery(`information_schema.statistics`).WillReturnRows(sqlmock.NewRows([]string{"t", "i", "n", "c", "p", "ty"}))
	mock.ExpectQuery(`referenced_table_name`).WillReturnRows(sqlmock.NewRows([]string{"t", "c", "col"}))
	mock.ExpectQuery(`@@performance_schema`).WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(0))
	mock.ExpectRollback()

	report, err := NewService(fakePools{mysql: db}, fakePermissions{doc: &domain.PermissionDocument{}}, Options{}).Run(context.Background(), "p1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if status := statusOf(report, CheckUnusedIndex); status.Status != StatusUnavailable {
		t.Fatalf("unused: %+v", status)
	}
}

// A catalog query that fails for any other reason fails the run: an
// incomplete report must not read as a clean one.
func TestCatalogFailureFailsTheRun(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`.`).WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()
	_, err := NewService(fakePools{postgres: db}, fakePermissions{doc: &domain.PermissionDocument{}}, Options{}).Run(context.Background(), "p1")
	if err == nil {
		t.Fatal("run succeeded")
	}
}

func TestOpenAndPermissionErrorsAreReturned(t *testing.T) {
	notServable := errors.New("not servable")
	if _, err := NewService(fakePools{err: notServable}, fakePermissions{}, Options{}).Run(context.Background(), "p1"); !errors.Is(err, notServable) {
		t.Fatalf("open error = %v", err)
	}
	db, _ := mockDB(t)
	storeDown := errors.New("platform db down")
	if _, err := NewService(fakePools{postgres: db}, fakePermissions{err: storeDown}, Options{}).Run(context.Background(), "p1"); !errors.Is(err, storeDown) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestDefaultLargeTableThreshold(t *testing.T) {
	if got := (Options{}).withDefaults().LargeTableRows; got != DefaultLargeTableRows {
		t.Fatalf("threshold = %d", got)
	}
}

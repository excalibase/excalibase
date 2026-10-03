package handler

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/tableimport"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type mockPools struct {
	db  *sql.DB
	err error
}

func (p mockPools) Open(context.Context, string) (*sql.DB, error) { return p.db, p.err }
func (mockPools) Evict(string)                                    {}
func (mockPools) ProjectStatusChanged(string, string)             {}

func targetOver(t *testing.T, storage string, err error) (TableLoader, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, mockErr := sqlmock.New()
	if mockErr != nil {
		t.Fatal(mockErr)
	}
	t.Cleanup(func() { db.Close() })
	instances := fakestore.NewInstances()
	_ = instances.Create(&domain.DatabaseInstance{ProjectID: importProject, StorageSize: storage})
	h := &SchemaHandler{pools: mockPools{db: db, err: err}, instances: instances}
	return h.NewImportTarget(tableimport.Loader{}), mock
}

func oneRow(t *testing.T) tableimport.RecordReader {
	src, err := tableimport.NewCSVSource(strings.NewReader("a\n1\n"), ',', smallImportLimits())
	if err != nil {
		t.Fatal(err)
	}
	return src
}

var oneColumn = tableimport.Options{Schema: "public", Table: "t", Mode: tableimport.ModeCreate, HasHeader: true,
	Columns: []tableimport.ColumnSpec{{Source: 0, Name: "a", Type: tableimport.TypeText}}}

// A disk that cannot take the import is refused before anything is written.
func TestImportTarget_RefusesAnImportTheDiskCannotHold(t *testing.T) {
	target, mock := targetOver(t, "1Gi", nil)
	mock.ExpectQuery("pg_database_size").WillReturnRows(sqlmock.NewRows([]string{"size"}).AddRow(int64(900 << 20)))
	_, err := target.Load(context.Background(), importProject, oneColumn, oneRow(t), 100<<20)
	if !errors.Is(err, tableimport.ErrDiskFull) {
		t.Fatalf("err = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestImportTarget_LoadsWhenThereIsRoom(t *testing.T) {
	target, mock := targetOver(t, "5Gi", nil)
	mock.ExpectQuery("pg_database_size").WillReturnRows(sqlmock.NewRows([]string{"size"}).AddRow(int64(10 << 20)))
	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE "public"."t"`).WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()
	_, err := target.Load(context.Background(), importProject, oneColumn, oneRow(t), 1<<20)
	if err == nil || !strings.Contains(err.Error(), "stop here") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportTarget_AnUnknownDiskSizeLeavesItToPostgres(t *testing.T) {
	target, mock := targetOver(t, "", nil)
	mock.ExpectBegin().WillReturnError(errors.New("no begin"))
	if _, err := target.Load(context.Background(), importProject, oneColumn, oneRow(t), 1<<20); err == nil {
		t.Fatal("expected the begin error")
	}
}

func TestImportTarget_RefusesBadProjectsAndClosedPools(t *testing.T) {
	target, _ := targetOver(t, "5Gi", errors.New("sealed"))
	if _, err := target.Load(context.Background(), "../x", oneColumn, oneRow(t), 1); err == nil {
		t.Fatal("an invalid project id was accepted")
	}
	if _, err := target.Load(context.Background(), importProject, oneColumn, oneRow(t), 1); err == nil {
		t.Fatal("a pool error was swallowed")
	}
}

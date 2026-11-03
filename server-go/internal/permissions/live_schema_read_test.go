package permissions

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func mockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func shapeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"name", "view", "column"}).
		AddRow("orders", false, "id").AddRow("orders", false, "status").
		AddRow("items", false, "id").AddRow("items", false, "order_id").
		AddRow("empty_view", true, nil)
}

func keyRows(schemaName string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"constraint", "ss", "st", "sc", "ts", "tt"}).
		AddRow("items_order", schemaName, "items", "order_id", schemaName, "orders")
}

func TestReadPostgresSchemaNamesRelationships(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectQuery("FROM pg_class").WithArgs("public").WillReturnRows(shapeRows())
	mock.ExpectQuery("FROM pg_constraint").WithArgs("public").WillReturnRows(keyRows("public"))
	live, err := ReadPostgresSchema(context.Background(), db, "public")
	if err != nil {
		t.Fatal(err)
	}
	source := func(context.Context, string) (*LiveSchema, error) { return live, nil }
	if err := CheckLive(context.Background(), source, "public.items", "select",
		[]byte(`{"filter":{"publicOrder":{"status":{"_eq":"x"}}},"columns":"*"}`)); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if err := CheckLive(context.Background(), source, "public.empty_view", "select", []byte(`{"filter":{},"columns":"*"}`)); err != nil {
		t.Fatalf("a view with no columns: %v", err)
	}
}

func TestReadMySQLSchemaReadsOnlyItsDatabase(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow("app"))
	mock.ExpectQuery("information_schema.columns").WithArgs("app").WillReturnRows(shapeRows())
	mock.ExpectQuery("information_schema.key_column_usage").WithArgs("app", "app").WillReturnRows(keyRows("app"))
	live, err := ReadMySQLSchema(context.Background(), db, "app")
	if err != nil || live.Database != "app" || len(live.tables) != 3 || live.tables["items"].relationships["appOrder"] != "orders" {
		t.Fatalf("live %+v %v", live, err)
	}

	other, mock := mockDB(t)
	mock.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow("app"))
	public, err := ReadMySQLSchema(context.Background(), other, "public")
	if err != nil || public.Database != "app" || len(public.tables) != 0 {
		t.Fatalf("public %+v %v", public, err)
	}
}

func TestReadSchemaReportsDatabaseErrors(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectQuery("FROM pg_class").WillReturnError(errors.New("boom"))
	if _, err := ReadPostgresSchema(context.Background(), db, "public"); err == nil || !strings.Contains(err.Error(), "read tables") {
		t.Fatalf("err = %v", err)
	}
	db, mock = mockDB(t)
	mock.ExpectQuery("FROM pg_class").WillReturnRows(shapeRows())
	mock.ExpectQuery("FROM pg_constraint").WillReturnError(errors.New("boom"))
	if _, err := ReadPostgresSchema(context.Background(), db, "public"); err == nil || !strings.Contains(err.Error(), "foreign keys") {
		t.Fatalf("err = %v", err)
	}
	db, mock = mockDB(t)
	mock.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow(nil))
	if _, err := ReadMySQLSchema(context.Background(), db, "app"); err == nil {
		t.Fatal("a connection without a database was read")
	}
	db, mock = mockDB(t)
	mock.ExpectQuery("SELECT DATABASE").WillReturnError(errors.New("boom"))
	if _, err := ReadMySQLSchema(context.Background(), db, "app"); err == nil {
		t.Fatal("a failed database read passed")
	}
}

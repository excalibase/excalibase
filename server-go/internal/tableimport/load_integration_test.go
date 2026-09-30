//go:build integration

package tableimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startTenant runs Postgres and returns a pool logged in as a non-superuser
// that owns nothing but may create in public, like excalibase_app.
func startTenant(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("tenant"), postgres.WithUsername("super"), postgres.WithPassword("superpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	superDSN, _ := container.ConnectionString(ctx, "sslmode=disable")
	superDB, err := sql.Open("postgres", superDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { superDB.Close() })
	if _, err := superDB.Exec(`CREATE ROLE app LOGIN PASSWORD 'apppass';
		GRANT CREATE, USAGE ON SCHEMA public TO app;
		CREATE SCHEMA sales AUTHORIZATION app;
		CREATE TABLE public.existing (id serial PRIMARY KEY, name text NOT NULL, qty integer);
		GRANT SELECT, INSERT ON public.existing TO app;
		GRANT USAGE ON SEQUENCE public.existing_id_seq TO app;`); err != nil {
		t.Fatal(err)
	}
	appDSN := strings.Replace(superDSN, "super:superpass", "app:apppass", 1)
	appDB, err := sql.Open("postgres", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appDB.Close() })
	return superDB, appDB
}

func testLoader() Loader {
	return Loader{StatementTimeout: 10 * time.Second, LockTimeout: 2 * time.Second, ChunkRows: 3, MaxRowErrors: 5}
}

func TestIntegration_CreateAndLoadInOneTransaction(t *testing.T) {
	superDB, appDB := startTenant(t)
	body := "Name,Age,Joined,Formula\nann,31,2026-01-02,=cmd|' /C calc'!A0\nbob,,2026-03-04,+1\n" +
		"cé,7,2026-05-06,x\n\"d, e\",8,2026-07-08,\"multi\nline\"\n"
	opts := Options{Schema: "public", Table: "people", Mode: ModeCreate, HasHeader: true, Columns: []ColumnSpec{
		{Source: 0, Name: "name", Type: TypeText}, {Source: 1, Name: "age", Type: TypeInteger},
		{Source: 2, Name: "joined", Type: TypeDate}, {Source: 3, Name: "formula", Type: TypeText},
	}}
	result, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 4 {
		t.Fatalf("rows = %d", result.Rows)
	}
	var formula, name, joined string
	var age sql.NullInt64
	if err := superDB.QueryRow(`SELECT name, age, joined::text, formula FROM public.people WHERE id = 1`).
		Scan(&name, &age, &joined, &formula); err != nil {
		t.Fatal(err)
	}
	if name != "ann" || age.Int64 != 31 || joined != "2026-01-02" || formula != "=cmd|' /C calc'!A0" {
		t.Fatalf("row 1 = %s %v %s %s", name, age, joined, formula)
	}
	var nulls, multi int
	_ = superDB.QueryRow(`SELECT count(*) FROM public.people WHERE age IS NULL`).Scan(&nulls)
	_ = superDB.QueryRow(`SELECT count(*) FROM public.people WHERE formula = E'multi\nline'`).Scan(&multi)
	if nulls != 1 || multi != 1 {
		t.Fatalf("nulls=%d multi=%d", nulls, multi)
	}
	var dataType string
	_ = superDB.QueryRow(`SELECT data_type FROM information_schema.columns WHERE table_name='people' AND column_name='id'`).Scan(&dataType)
	if dataType != "bigint" {
		t.Fatalf("id type = %s", dataType)
	}
}

// Nothing is granted: a new table is reachable by no end-user role until
// someone adds a permission (Hasura model).
func TestIntegration_TheNewTableGrantsNothing(t *testing.T) {
	superDB, appDB := startTenant(t)
	opts := Options{Schema: "public", Table: "quiet", Mode: ModeCreate, HasHeader: true,
		Columns: []ColumnSpec{{Source: 0, Name: "a", Type: TypeText}}}
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "a\nx\n")); err != nil {
		t.Fatal(err)
	}
	var grants int
	_ = superDB.QueryRow(`SELECT count(*) FROM information_schema.role_table_grants
		WHERE table_name = 'quiet' AND grantee <> 'app'`).Scan(&grants)
	if grants != 0 {
		t.Fatalf("%d grants on the imported table", grants)
	}
}

func TestIntegration_RowErrorsRollEverythingBack(t *testing.T) {
	superDB, appDB := startTenant(t)
	var b strings.Builder
	b.WriteString("n\n")
	for i := 0; i < 10; i++ {
		b.WriteString(fmt.Sprintf("%d\n", i))
	}
	b.WriteString("x1\n5\nx2\n")
	opts := Options{Schema: "public", Table: "nums", Mode: ModeCreate, HasHeader: true,
		Columns: []ColumnSpec{{Source: 0, Name: "n", Type: TypeInteger}}}
	_, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, b.String()))
	var rowErrs *RowErrors
	if !errors.As(err, &rowErrs) || len(rowErrs.Errors) != 2 || rowErrs.Errors[0].Line != 12 || rowErrs.Errors[1].Line != 14 {
		t.Fatalf("err = %v (%+v)", err, rowErrs)
	}
	var exists bool
	_ = superDB.QueryRow(`SELECT to_regclass('public.nums') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("a failed import left its table behind")
	}
}

// A value only Postgres can judge (a date) fails in COPY; the error still
// names the line of the file, not the line of the chunk.
func TestIntegration_ACopyErrorNamesTheFileLine(t *testing.T) {
	_, appDB := startTenant(t)
	body := "d\n2026-01-01\n2026-01-02\n2026-01-03\n2026-01-04\nnot a date\n"
	opts := Options{Schema: "public", Table: "dates", Mode: ModeCreate, HasHeader: true,
		Columns: []ColumnSpec{{Source: 0, Name: "d", Type: TypeDate}}}
	_, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, body))
	var rowErrs *RowErrors
	if !errors.As(err, &rowErrs) || rowErrs.Errors[0].Line != 6 {
		t.Fatalf("err = %v (%+v)", err, rowErrs)
	}
}

func TestIntegration_AppendToAnExistingTable(t *testing.T) {
	superDB, appDB := startTenant(t)
	opts := Options{Schema: "public", Table: "existing", Mode: ModeAppend, HasHeader: true, Columns: []ColumnSpec{
		{Source: 0, Name: "name", Type: TypeText}, {Source: 1, Name: "qty", Type: TypeInteger},
	}}
	result, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "name,qty\na,1\nb,2\n"))
	if err != nil || result.Rows != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var count int
	_ = superDB.QueryRow(`SELECT count(*) FROM public.existing`).Scan(&count)
	if count != 2 {
		t.Fatalf("count = %d", count)
	}
	opts.Columns[1].Name = "missing"
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "name,qty\na,1\n")); !errors.Is(err, ErrColumnsMissing) {
		t.Fatalf("err = %v", err)
	}
	opts.Table = "nope"
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "name,qty\na,1\n")); !errors.Is(err, ErrTableMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegration_CreateRefusesAnExistingTable(t *testing.T) {
	_, appDB := startTenant(t)
	opts := Options{Schema: "public", Table: "existing", Mode: ModeCreate, HasHeader: true,
		Columns: []ColumnSpec{{Source: 0, Name: "a", Type: TypeText}}}
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "a\nx\n")); !errors.Is(err, ErrTableExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegration_AnotherSchemaAndAChosenPrimaryKey(t *testing.T) {
	superDB, appDB := startTenant(t)
	opts := Options{Schema: "sales", Table: "orders", Mode: ModeCreate, HasHeader: true, PrimaryKey: "code",
		Columns: []ColumnSpec{{Source: 0, Name: "code", Type: TypeText}, {Source: 1, Name: "total", Type: TypeNumeric}}}
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "code,total\nA1,1.50\n")); err != nil {
		t.Fatal(err)
	}
	var total string
	_ = superDB.QueryRow(`SELECT total::text FROM sales.orders WHERE code = 'A1'`).Scan(&total)
	if total != "1.50" {
		t.Fatalf("total = %q", total)
	}
	if _, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "code,total\nA1,1\n")); !errors.Is(err, ErrTableExists) {
		t.Fatalf("second create: %v", err)
	}
	opts.Mode = ModeAppend
	_, err := testLoader().Load(context.Background(), appDB, opts, csvSource(t, "code,total\nA1,2\n"))
	var rowErrs *RowErrors
	if !errors.As(err, &rowErrs) || !strings.Contains(rowErrs.Errors[0].Message, "duplicate") {
		t.Fatalf("duplicate key: %v", err)
	}
}

// The platform's statement timeout bounds the load; a tenant cannot lift it.
func TestIntegration_TheStatementTimeoutBoundsTheLoad(t *testing.T) {
	superDB, appDB := startTenant(t)
	if _, err := superDB.Exec(`CREATE TABLE public.slow (a text);
		GRANT INSERT, SELECT ON public.slow TO app;
		CREATE FUNCTION public.slow_insert() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN PERFORM pg_sleep(3); RETURN NEW; END';
		CREATE TRIGGER slow BEFORE INSERT ON public.slow FOR EACH ROW EXECUTE FUNCTION public.slow_insert();`); err != nil {
		t.Fatal(err)
	}
	loader := testLoader()
	loader.StatementTimeout = time.Second
	opts := Options{Schema: "public", Table: "slow", Mode: ModeAppend, HasHeader: true,
		Columns: []ColumnSpec{{Source: 0, Name: "a", Type: TypeText}}}
	start := time.Now()
	_, err := loader.Load(context.Background(), appDB, opts, csvSource(t, "a\nx\n"))
	if !errors.Is(err, ErrTimedOut) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

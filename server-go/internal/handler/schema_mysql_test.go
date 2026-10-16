package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// mysqlSchemaRouter serves the schema routes for one ACTIVE MySQL project.
func mysqlSchemaRouter(t *testing.T, pools *fakeProjectDB) http.Handler {
	t.Helper()
	instances := fakestore.NewInstances()
	if err := instances.Create(&domain.DatabaseInstance{ProjectID: "proj-my", Status: string(domain.StatusActive), DBType: domain.MySQL}); err != nil {
		t.Fatal(err)
	}
	h := NewSchemaHandler(pools, time.Second)
	h.SetInstanceStore(instances)
	r := chi.NewRouter()
	r.Route("/api/schema", h.Routes)
	return r
}

// EXC-619: a MySQL project's tables are read from its MySQL database.
func TestSchemaHandler_ReadsAMySQLProjectThroughTheMySQLCatalog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT DATABASE\(\)`).WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow("app"))
	mock.ExpectQuery(`FROM information_schema.tables\s+WHERE table_schema = \?`).WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"name", "schema", "type", "comment"}).AddRow("orders", "app", "BASE TABLE", nil))
	pools := &fakeProjectDB{mysqlDB: db}

	w := httptest.NewRecorder()
	mysqlSchemaRouter(t, pools).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/schema/proj-my/tables?schema=public", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"orders","schema":"app"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(pools.opened) != 0 || len(pools.mysqlOpened) != 1 || pools.mysqlOpened[0] != (mysqlOpen{"proj-my", false}) {
		t.Fatalf("postgres %v, mysql %v", pools.opened, pools.mysqlOpened)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The SQL runner and /ddl take scripts; every other route a single statement.
func TestSchemaHandler_RunsMySQLScriptsOnTheScriptPool(t *testing.T) {
	pools := &fakeProjectDB{}
	router := mysqlSchemaRouter(t, pools)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/schema/proj-my/query", strings.NewReader(`{"query":"SELECT 1; SELECT 2"}`)),
		httptest.NewRequest(http.MethodPost, "/api/schema/proj-my/ddl", strings.NewReader(`{"sql":"CREATE TABLE t (a int)"}`)),
		httptest.NewRequest(http.MethodGet, "/api/schema/proj-my/tables/orders/rows", nil),
	} {
		router.ServeHTTP(httptest.NewRecorder(), request)
	}
	want := []mysqlOpen{{"proj-my", true}, {"proj-my", true}, {"proj-my", false}}
	if len(pools.mysqlOpened) != len(want) {
		t.Fatalf("opened %v", pools.mysqlOpened)
	}
	for index := range want {
		if pools.mysqlOpened[index] != want[index] {
			t.Errorf("request %d opened %v, want %v", index, pools.mysqlOpened[index], want[index])
		}
	}
}

package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/go-chi/chi/v5"
)

const schedulerTestMaster = "scheduler-master-secret"

func schedulerRouteHandler(t *testing.T, db *sql.DB) (*FunctionHandler, chi.Router) {
	t.Helper()
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, nil, nil, "")
	h.runtimeSecret = schedulerTestMaster
	h.SetSchedulerDBFn(func(context.Context, string) (*sql.DB, error) {
		if db == nil {
			return nil, errors.New("no database in this test")
		}
		return db, nil
	})
	r := chi.NewRouter()
	r.Post("/internal/runtime/projects/{projectId}/scheduler", h.InternalSchedule)
	return h, r
}

func postSchedule(r chi.Router, projectID, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/runtime/projects/"+projectID+"/scheduler", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(runtimeTokenHeader, token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const oneSchedule = `{"ops":[{"op":"schedule","id":"abc123","moduleName":"jobs","exportName":"send","args":{"to":"ada"},"scheduledForMs":1700000000000}]}`

func TestInternalSchedule_RefusesWithoutThisProjectsToken(t *testing.T) {
	_, r := schedulerRouteHandler(t, nil)
	for name, token := range map[string]string{
		"no token":            "",
		"master secret":       schedulerTestMaster,
		"other project token": edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_other"),
	} {
		if w := postSchedule(r, "proj_a", token, oneSchedule); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, w.Code)
		}
	}
}

func TestInternalSchedule_FailsClosedWithoutARuntimeSecret(t *testing.T) {
	h, r := schedulerRouteHandler(t, nil)
	h.runtimeSecret = ""
	if w := postSchedule(r, "proj_a", "anything", oneSchedule); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestInternalSchedule_RefusesOpsTheSweepWouldNotRun(t *testing.T) {
	_, r := schedulerRouteHandler(t, nil)
	token := edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a")
	cases := map[string]string{
		"not json":         `{`,
		"no ops":           `{"ops":[]}`,
		"unknown op":       `{"ops":[{"op":"drop","id":"abc"}]}`,
		"module climbs":    `{"ops":[{"op":"schedule","id":"abc","moduleName":"../x","exportName":"send","args":{},"scheduledForMs":1}]}`,
		"bad export":       `{"ops":[{"op":"schedule","id":"abc","moduleName":"jobs","exportName":"a-b","args":{},"scheduledForMs":1}]}`,
		"empty id":         `{"ops":[{"op":"cancel","id":""}]}`,
		"long id":          `{"ops":[{"op":"cancel","id":"` + strings.Repeat("a", 65) + `"}]}`,
		"no time":          `{"ops":[{"op":"schedule","id":"abc","moduleName":"jobs","exportName":"send","args":{}}]}`,
		"unknown field":    `{"ops":[{"op":"cancel","id":"abc","projectId":"proj_b"}]}`,
		"too many ops":     `{"ops":[` + strings.TrimSuffix(strings.Repeat(`{"op":"cancel","id":"a"},`, maxScheduleOps+1), ",") + `]}`,
		"bad project path": ``,
	}
	for name, body := range cases {
		project := "proj_a"
		if name == "bad project path" {
			project, body = "Proj..A", oneSchedule
		}
		if w := postSchedule(r, project, token, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
}

// The project comes from the authenticated path, never the body, and a
// cancel only reaches that project's pending rows.
func TestInternalSchedule_WritesTheOpsInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, r := schedulerRouteHandler(t, db)
	when := time.UnixMilli(1700000000000).UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("octet_length(($1::jsonb)::text)")).
		WithArgs(`{"to":"ada"}`, scheduler.DefaultMaxArgsBytes).
		WillReturnRows(sqlmock.NewRows([]string{"too_large"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO excalibase.excalibase_scheduled_functions")).
		WithArgs("abc123", "proj_a", "jobs", "send", `{"to":"ada"}`, when).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE excalibase.excalibase_scheduled_functions")).
		WithArgs("abc123", "proj_a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	body := `{"ops":[{"op":"schedule","id":"abc123","moduleName":"jobs","exportName":"send","args":{"to":"ada"},"scheduledForMs":1700000000000},{"op":"cancel","id":"abc123"}]}`
	w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Wire whitespace is not content: it is compacted away before the args reach
// Postgres, so an indented-but-small payload is accepted.
func TestInternalSchedule_CompactsWhitespaceBeforeMeasuring(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, r := schedulerRouteHandler(t, db)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("octet_length(($1::jsonb)::text)")).
		WithArgs(`{"to":"ada"}`, scheduler.DefaultMaxArgsBytes).
		WillReturnRows(sqlmock.NewRows([]string{"too_large"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO excalibase.excalibase_scheduled_functions")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	padding := strings.Repeat(" ", scheduler.DefaultMaxArgsBytes)
	body := `{"ops":[{"op":"schedule","id":"abc123","moduleName":"jobs","exportName":"send","scheduledForMs":1,"args":` + padding + `{"to":"ada"}` + padding + `}]}`
	if w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), body); w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The jsonb-text size is what the sweep measures; when Postgres reports the
// args over the limit the whole request is a 400, never a silent close.
func TestInternalSchedule_RejectsArgsTheSweepWouldClose(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, r := schedulerRouteHandler(t, db)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("octet_length(($1::jsonb)::text)")).
		WillReturnRows(sqlmock.NewRows([]string{"too_large"}).AddRow(true))
	mock.ExpectRollback()

	body := `{"ops":[{"op":"schedule","id":"abc123","moduleName":"jobs","exportName":"send","scheduledForMs":1,"args":{"a":"b"}}]}`
	if w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), body); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInternalSchedule_RollsBackWhenAWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, r := schedulerRouteHandler(t, db)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("octet_length(($1::jsonb)::text)")).
		WillReturnRows(sqlmock.NewRows([]string{"too_large"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO excalibase.excalibase_scheduled_functions")).
		WillReturnError(errors.New("pq: connection reset with internal detail"))
	mock.ExpectRollback()

	w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), oneSchedule)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "internal detail") {
		t.Fatalf("the database error reached the runtime: %s", w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInternalSchedule_UnavailableWithoutTheSchedulerLogin(t *testing.T) {
	h, r := schedulerRouteHandler(t, nil)
	h.schedulerDBFn = nil
	w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), oneSchedule)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestInternalSchedule_UnavailableWhenTheSchedulerLoginCannotOpen(t *testing.T) {
	// schedulerRouteHandler(nil) files a schedulerDBFn that errors, standing
	// in for a login the platform could not open.
	_, r := schedulerRouteHandler(t, nil)
	w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), oneSchedule)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
}

func TestInternalSchedule_RefusesAScheduleWithNoArgs(t *testing.T) {
	_, r := schedulerRouteHandler(t, nil)
	body := `{"ops":[{"op":"schedule","id":"abc","moduleName":"jobs","exportName":"send","scheduledForMs":1}]}`
	if w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), body); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

// A database that cannot begin, or cannot measure the args, is a 500 that
// names no internal detail.
func TestInternalSchedule_DatabaseFailuresAre500s(t *testing.T) {
	for name, arrange := range map[string]func(sqlmock.Sqlmock){
		"begin fails": func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(errors.New("internal begin detail")) },
		"size check fails": func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectQuery(regexp.QuoteMeta("octet_length")).WillReturnError(errors.New("internal size detail"))
			m.ExpectRollback()
		},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		arrange(mock)
		_, r := schedulerRouteHandler(t, db)
		w := postSchedule(r, "proj_a", edgefn.DeriveRuntimeSecret(schedulerTestMaster, "proj_a"), oneSchedule)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "internal") && strings.Contains(w.Body.String(), "detail") {
			t.Errorf("%s: status %d body %s", name, w.Code, w.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		db.Close()
	}
}

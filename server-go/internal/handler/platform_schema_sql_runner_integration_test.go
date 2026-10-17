//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/go-chi/chi/v5"
)

// EXC-623: Studio's SQL runner (and MCP execute_sql, which calls the same
// route) runs customer SQL as excalibase_app. Against a database registered
// with the platform's own role SQL, it reads the project's auth users and
// works in its own schemas, and every write to the excalibase schema is
// refused by Postgres.
func TestStudioSQLRunner_PlatformSchemaIsReadOnlyToCustomerSQL(t *testing.T) {
	db, creds := startProjectPG(t)
	opener := wiringOpener(t, creds, "ACTIVE")
	authAdmin := openWiringDB(t, creds["auth_admin"])
	if _, err := authAdmin.Exec(`CREATE TABLE auth.users (id serial PRIMARY KEY, email text);
		INSERT INTO auth.users (email) VALUES ('ada@example.test')`); err != nil {
		t.Fatalf("auth migration: %v", err)
	}

	r := chi.NewRouter()
	r.Route(testSchemaBase, NewSchemaHandler(opener, 30*time.Second).Routes)
	run := func(statement string) (int, schema.QueryResult) {
		body, _ := json.Marshal(map[string]string{"query": statement})
		w := schemaRequest(r, http.MethodPost, testSchemaBase+"/"+wiringProjectID+"/query", string(body))
		var result schema.QueryResult
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if result.Error == "" && w.Code != http.StatusOK {
			result.Error = w.Body.String()
		}
		return w.Code, result
	}

	for _, statement := range []string{
		`CREATE TABLE excalibase.planted (id int)`,
		`CREATE FUNCTION excalibase.planted() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'`,
		`CREATE OR REPLACE FUNCTION excalibase.set_realtime_table(target_schema text, target_table text, publish boolean) RETURNS void LANGUAGE sql AS ''`,
		`INSERT INTO excalibase.excalibase_scheduled_functions (id, project_id, module_name, export_name, args, scheduled_for) VALUES ('t', 'p', 'm', 'e', '{}', now())`,
		`DELETE FROM excalibase.excalibase_cron_jobs`,
		`ALTER TABLE excalibase.excalibase_cron_jobs OWNER TO excalibase_app`,
		`INSERT INTO auth.users (email) VALUES ('mallory@example.test')`,
	} {
		if code, result := run(statement); code == http.StatusOK && result.Error == "" {
			t.Errorf("customer SQL was allowed: %s", statement)
		} else if !strings.Contains(result.Error, "permission denied") && !strings.Contains(result.Error, "must be owner") {
			t.Errorf("%s refused for another reason (%d): %s", statement, code, result.Error)
		}
	}

	if code, result := run(`SELECT email FROM auth.users`); code != http.StatusOK || result.Error != "" || len(result.Rows) != 1 {
		t.Fatalf("reading the project's auth users: %d %+v", code, result)
	}
	for _, statement := range []string{
		`CREATE TABLE public.notes (id serial PRIMARY KEY, body text)`,
		`INSERT INTO public.notes (body) VALUES ('hello')`,
	} {
		if code, result := run(statement); code != http.StatusOK || result.Error != "" {
			t.Fatalf("customer SQL in its own schema failed: %s: %d %s", statement, code, result.Error)
		}
	}
	var owner string
	if err := db.QueryRow(`SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'excalibase.excalibase_cron_jobs'::regclass`).Scan(&owner); err != nil || owner != "postgres" {
		t.Fatalf("cron table owner = %q (%v)", owner, err)
	}
}

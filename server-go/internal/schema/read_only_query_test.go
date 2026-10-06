package schema

import "testing"

func TestSideEffectCall(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM orders":                                  "",
		"SELECT pg_terminate_backend(42)":                       "pg_terminate_backend",
		"select PG_CANCEL_BACKEND (42)":                         "pg_cancel_backend",
		`SELECT "pg_terminate_backend"(42)`:                     "pg_terminate_backend",
		"SELECT pg_catalog.pg_reload_conf()":                    "pg_reload_conf",
		"SELECT dblink_exec('x', 'delete from t')":              "dblink_exec",
		"SELECT * FROM dblink('x', 'select 1') AS t(a int)":     "dblink",
		"SELECT lo_export(1, '/tmp/x')":                         "lo_export",
		"SELECT pg_notify('c', 'x')":                            "pg_notify",
		"SELECT lo_count FROM stats":                            "",
		"SELECT 'pg_terminate_backend(1)' AS just_text":         "",
		"SELECT name FROM functions WHERE name = 'dblink_exec'": "",
	}
	for statement, want := range cases {
		if got := sideEffectCall(statement); got != want {
			t.Errorf("%q: got %q want %q", statement, got, want)
		}
	}
}

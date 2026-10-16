package schema

import "testing"

func TestMySQLReadOnlyGate(t *testing.T) {
	for _, query := range []string{"SELECT 1", "  with x as (select 1) select * from x", "SHOW TABLES", "EXPLAIN SELECT 1", "DESCRIBE orders", "desc orders", "TABLE orders", "VALUES ROW(1)", "/* hi */ SELECT 1", "-- note\nSELECT 1"} {
		if reason := mysqlReadOnlyRefusal(query); reason != "" {
			t.Errorf("%q refused: %s", query, reason)
		}
	}
	for _, query := range []string{"INSERT INTO t VALUES (1)", "CREATE TEMPORARY TABLE t (a int)", "SET SESSION max_execution_time = 0", "SELECT * FROM t INTO OUTFILE '/tmp/x'", "select 1 into dumpfile '/tmp/y'", "DO SLEEP(1)", "CALL p()"} {
		if reason := mysqlReadOnlyRefusal(query); reason == "" {
			t.Errorf("%q was let through", query)
		}
	}
}

func TestMySQLScriptReturnsRows(t *testing.T) {
	for query, want := range map[string]bool{
		"SELECT 1":                             true,
		"/* c */ show tables":                  true,
		"CREATE TABLE t (a int); SELECT 1":     false,
		"INSERT INTO t VALUES (1)":             false,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
	} {
		if got := mysqlScriptReturnsRows(query); got != want {
			t.Errorf("%q: got %v", query, got)
		}
	}
}

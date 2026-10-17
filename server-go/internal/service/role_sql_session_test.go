package service

import (
	"strings"
	"testing"
)

func TestSuperuserPsqlCommandPinsTheSession(t *testing.T) {
	cmd := superuserPsqlCommand("app", "15", "SELECT 1;", "SELECT 2;")
	joined := strings.Join(cmd, "\x00")
	for _, want := range []string{"-X", "ON_ERROR_STOP=1", "-c role=none", "-c default_transaction_read_only=off",
		"-c statement_timeout=0", "-c lock_timeout=0", "-c plpgsql.extra_errors=none", "-c exit_on_error=off"} {
		if !strings.Contains(joined, want) {
			t.Errorf("command lacks %q: %q", want, cmd)
		}
	}
	if strings.Contains(joined, "transaction_timeout=0'") || strings.Contains(joined, "-c transaction_timeout") {
		t.Error("Postgres 15 refuses a connection that names transaction_timeout")
	}
	scripts := 0
	for i, arg := range cmd {
		if arg == "-c" {
			scripts++
			if !strings.Contains(cmd[i+1], "current_user <> session_user") {
				t.Errorf("script %d does not check it runs as the superuser itself", scripts)
			}
		}
	}
	if scripts != 2 {
		t.Fatalf("scripts = %d, want each as its own transaction", scripts)
	}
}

func TestSuperuserPsqlCommandPinsTransactionTimeoutFrom17(t *testing.T) {
	if !strings.Contains(superuserConninfo("app", "17"), "-c transaction_timeout=0") {
		t.Fatal("Postgres 17 has transaction_timeout and the owner can set it on the database")
	}
}

func TestSuperuserConninfoQuotesTheDatabaseName(t *testing.T) {
	got := superuserConninfo(`a'b\c`, "16")
	if !strings.HasPrefix(got, `dbname='a\'b\\c' options='`) {
		t.Fatalf("conninfo = %s", got)
	}
}

func TestRoleScriptsTakeTheSchemaBackFirst(t *testing.T) {
	first := reservedSchemaFirstSQL()
	for _, want := range []string{"excalibase_reserved_rebuild", "ALTER SCHEMA excalibase OWNER TO CURRENT_USER", "REVOKE ALL ON ALL TABLES IN SCHEMA excalibase"} {
		if !strings.Contains(first, want) {
			t.Errorf("first transaction lacks %q", want)
		}
	}
	if strings.Contains(first, "excalibase_squat") {
		t.Error("the squat guard must not run in the first transaction")
	}
}

package service

import (
	"strings"
	"testing"
)

func TestSuperuserPsqlCommandPinsTheSession(t *testing.T) {
	cmd := superuserPsqlCommand("app", 150000, "SELECT 1;", "SELECT 2;")
	joined := strings.Join(cmd, "\x00")
	for _, want := range []string{"-X", "ON_ERROR_STOP=1", "-c role=none", "-c default_transaction_read_only=off",
		"-c statement_timeout=0", "-c lock_timeout=", "-c plpgsql.extra_errors=none", "-c exit_on_error=off",
		"-c local_preload_libraries=", "-c session_preload_libraries="} {
		if !strings.Contains(joined, want) {
			t.Errorf("command lacks %q: %q", want, cmd)
		}
	}
	// A customer that locks an excalibase object must not be able to block the
	// rebuild forever, so lock_timeout is finite, never 0.
	if strings.Contains(joined, "-c lock_timeout=0") {
		t.Error("lock_timeout is pinned to 0, so a held lock blocks the rebuild forever")
	}
	if strings.Contains(joined, "-c transaction_timeout") {
		t.Error("Postgres 15 refuses a connection that names transaction_timeout")
	}
	scripts := 0
	for i, arg := range cmd {
		if arg == "-c" && strings.Contains(cmd[i+1], "current_user <> session_user") {
			scripts++
		}
	}
	if scripts != 2 {
		t.Fatalf("guarded scripts = %d, want each as its own transaction", scripts)
	}
}

func TestSuperuserPsqlCommandPinsTransactionTimeoutFrom17(t *testing.T) {
	if !strings.Contains(superuserConninfo("app", 170004), "-c transaction_timeout=0") {
		t.Fatal("Postgres 17 has transaction_timeout and the owner can set it on the database")
	}
	if strings.Contains(superuserConninfo("app", 0), "transaction_timeout") {
		t.Fatal("an unknown server version must not name transaction_timeout")
	}
}

func TestSuperuserConninfoQuotesTheDatabaseName(t *testing.T) {
	got := superuserConninfo(`a'b\c`, 160000)
	if !strings.HasPrefix(got, `dbname='a\'b\\c' options='`) {
		t.Fatalf("conninfo = %s", got)
	}
}

func TestServerVersionProbeNamesNoVersionedSetting(t *testing.T) {
	cmd := serverVersionProbeCommand("app")
	joined := strings.Join(cmd, "\x00")
	if !strings.Contains(joined, "server_version_num") {
		t.Fatalf("probe does not read the version: %q", cmd)
	}
	if strings.Contains(joined, "transaction_timeout") {
		t.Fatal("the probe runs before the version is known, so it must not name transaction_timeout")
	}
	if !strings.Contains(joined, "-c local_preload_libraries=") {
		t.Fatal("the probe connection must also survive a poisoned local_preload_libraries")
	}
}

func TestRoleScriptsTakeTheSchemaBackFirst(t *testing.T) {
	first := reservedSchemaFirstSQL()
	for _, want := range []string{
		"excalibase_reserved_rebuild",
		"ALTER SCHEMA excalibase OWNER TO CURRENT_USER",
		"REVOKE ALL ON ALL TABLES IN SCHEMA excalibase",
		// The CREATE EXTENSION guard must land in the first transaction, so a
		// squat that aborts the second one does not leave it uninstalled.
		"excalibase_guard_reserved_schemas",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("first transaction lacks %q", want)
		}
	}
	if strings.Contains(first, "excalibase_squat") {
		t.Error("the squat guard must not run in the first transaction")
	}
}

func TestReservedSchemaBlockersTerminateOnlyCustomerSessions(t *testing.T) {
	cmd := terminateReservedSchemaBlockersCommand("app")
	sql := cmd[len(cmd)-1]
	for _, want := range []string{"pg_terminate_backend", "pg_backend_pid()", "rolsuper", "excalibase", "excalibase_app", "auth_admin", "excalibase_scheduler", "cdc_watcher"} {
		if !strings.Contains(sql, want) {
			t.Errorf("terminate SQL lacks %q", want)
		}
	}
}

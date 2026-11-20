package service

import (
	"strings"
	"testing"
)

func TestSuperuserPsqlCommandPinsTheSession(t *testing.T) {
	cmd := superuserPsqlCommand("app", 150000)
	joined := strings.Join(cmd, "\x00")
	for _, want := range []string{"-X", "ON_ERROR_STOP=1", "VERBOSITY=verbose", "-c role=none", "-c default_transaction_read_only=off",
		"-c statement_timeout=0", "-c lock_timeout=", "-c plpgsql.extra_errors=none", "-c exit_on_error=off",
		"-c local_preload_libraries=", "-c session_preload_libraries=",
		"-c password_encryption=scram-sha-256", "-c default_tablespace=", "-c temp_tablespaces=",
		"-c log_statement=none", "-c log_min_error_statement=panic", "-c log_min_duration_statement=-1",
		"-c log_min_duration_sample=-1", "-c log_transaction_sample_rate=0", "-c log_error_verbosity=terse", "-c pgaudit.log=none"} {
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
}

// Exec arguments land in the exec URL, which the API server audits, so the
// script travels on stdin, each one its own guarded transaction.
func TestSuperuserScriptKeepsEachScriptItsOwnGuardedTransaction(t *testing.T) {
	if joined := strings.Join(superuserPsqlCommand("app", 150000), "\x00"); !strings.HasSuffix(joined, "-f\x00-") || strings.Contains(joined, "\x00-c\x00") {
		t.Fatalf("the command does not read its script from stdin: %q", joined)
	}
	input := superuserScript("SELECT 1;", "SELECT 2;")
	first := strings.Index(input, "SELECT 1;")
	second := strings.Index(input, "SELECT 2;")
	if first < 0 || second < first {
		t.Fatalf("the scripts are not in order: %s", input)
	}
	for name, part := range map[string]string{"first": input[:second], "second": input[first+len("SELECT 1;"):]} {
		if !strings.Contains(part, "BEGIN;") || !strings.Contains(part, "COMMIT;") {
			t.Errorf("%s script is not its own transaction", name)
		}
	}
	if strings.Count(input, "current_user <> session_user") != 2 {
		t.Error("each script must start with the session guard")
	}
	if strings.Count(input, "BEGIN;\n") != 2 || strings.Count(input, "\nCOMMIT;\n") != 2 {
		t.Errorf("want two transactions: %s", input)
	}
}

// Only role SQL names verifiers; a probe's failure stays in the operator's log.
func TestOnlyRoleSQLSilencesTheServerLog(t *testing.T) {
	for name, cmd := range map[string][]string{"version probe": serverVersionProbeCommand("app"), "terminate": terminateNonServiceBackendsCommand("app")} {
		if strings.Contains(strings.Join(cmd, " "), "log_statement") {
			t.Errorf("the %s silences the server log", name)
		}
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
	if strings.Contains(first, "$excalibase_squat$") {
		t.Error("the aborting squat guard must not run in the first transaction")
	}
}

func TestTerminateSparesServicesButNotTheAppRoleOrOwner(t *testing.T) {
	cmd := terminateNonServiceBackendsCommand("app")
	sql := cmd[len(cmd)-1]
	for _, want := range []string{"pg_terminate_backend", "pg_backend_pid()", "rolsuper", "client backend", "auth_admin", "excalibase_scheduler", "cdc_watcher", "streaming_replica"} {
		if !strings.Contains(sql, want) {
			t.Errorf("terminate SQL lacks %q", want)
		}
	}
	// excalibase_app is customer SQL's own role, and admin can be an owner
	// login name; neither may be spared.
	spared := sql[strings.Index(sql, "ARRAY["):]
	for _, mustKill := range []string{"'excalibase_app'", "'admin'"} {
		if strings.Contains(spared, mustKill) {
			t.Errorf("terminate SQL spares %s", mustKill)
		}
	}
}

func TestSuperuserConninfoPinsScramIterationsFrom16(t *testing.T) {
	if !strings.Contains(superuserConninfo("app", 160004), "-c scram_iterations=4096") {
		t.Fatal("Postgres 16 has scram_iterations and the owner can lower it")
	}
	if strings.Contains(superuserConninfo("app", 150000), "scram_iterations") {
		t.Fatal("Postgres 15 refuses a connection that names scram_iterations")
	}
}

// A service name is spared only while the platform holds it: a role some
// non-superuser administers (on 16+ the owner gets ADMIN on what it creates)
// is a squat, and its backends are terminated like any customer's.
func TestTerminateTreatsASquattedServiceNameAsACustomer(t *testing.T) {
	sql := terminateNonServiceBackendsSQL
	for _, want := range []string{"pg_auth_members", "admin_option", "NOT u.rolsuper"} {
		if !strings.Contains(sql, want) {
			t.Errorf("terminate SQL lacks the squat check %q", want)
		}
	}
}

// The first script reports a squat early but never aborts on it.
func TestFirstScriptWarnsOfASquatWithoutAborting(t *testing.T) {
	first := reservedSchemaFirstSQL()
	if !strings.Contains(first, "excalibase_squat_warning") || !strings.Contains(first, "RAISE WARNING") {
		t.Fatal("the first script does not report a squatted platform role")
	}
	if strings.Contains(first, "$excalibase_squat$") {
		t.Fatal("the aborting squat guard must stay out of the first script")
	}
}

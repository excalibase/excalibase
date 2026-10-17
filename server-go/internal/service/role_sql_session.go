package service

import (
	"strings"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// The role SQL runs as the superuser in a database the customer owns, and an
// owner can attach settings to that database with ALTER DATABASE: a role to
// switch to, read-only transactions, timeouts, PL/pgSQL extra errors, and —
// the nastiest — a preload library, which makes every new backend FATAL
// before it runs a line, the superuser's reconcile included. Any of them
// would leave a project on whatever grants it had (EXC-623). Client startup
// options outrank per-database settings, so the command pins every setting
// the SQL depends on, and every connect-time setting a non-superuser could
// poison.
var pinnedSessionOptions = []string{
	// A poisoned preload library FATALs the connection at startup; pinned
	// empty, the owner's value never loads. session_preload_libraries is
	// superuser-only, but we connect as the superuser, so pin it too.
	"local_preload_libraries=",
	"session_preload_libraries=",
	"role=none",
	"default_transaction_read_only=off",
	// Passwords are set by the role SQL; pin the hash so an owner's
	// password_encryption=md5 cannot weaken them, and keep CREATE off an
	// owner-chosen or invalid tablespace.
	"password_encryption=scram-sha-256",
	"default_tablespace=",
	"temp_tablespaces=",
	"statement_timeout=0",
	// Finite, never 0: a customer holding a lock on an excalibase object (or
	// a view over one) must not block the rebuild forever. On a timeout the
	// caller terminates the customer's backends and retries.
	"lock_timeout=3s",
	"idle_in_transaction_session_timeout=0",
	"idle_session_timeout=0",
	"exit_on_error=off",
	"standard_conforming_strings=on",
	"transform_null_equals=off",
	"plpgsql.extra_errors=none",
	"search_path=pg_catalog,pg_temp",
}

// transactionTimeoutMinVersion is the server_version_num of the first release
// with transaction_timeout (17.0). Naming it on an older server FATALs the
// connection, so it is pinned only when the probe reports at least this.
const transactionTimeoutMinVersion = 170000

// scramIterationsMinVersion is the server_version_num of the first release
// with scram_iterations (16.0); older servers FATAL when a client names it.
const scramIterationsMinVersion = 160000

// sessionGuardSQL starts every script: it refuses to go on unless the session
// is the superuser itself, not a role the session was switched to, in a
// writable transaction. It also turns transaction_timeout off for the
// transaction where the server has it, in case the connect-time option could
// not name it (an unknown server version).
const sessionGuardSQL = pgroles.PinnedSearchPath + `
DO $excalibase_session$
BEGIN
  PERFORM set_config('transaction_timeout', '0', true) FROM pg_settings WHERE name = 'transaction_timeout';
  IF current_user <> session_user
     OR NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)
     OR current_setting('transaction_read_only')::boolean THEN
    RAISE EXCEPTION 'platform role SQL must run as the superuser itself in a writable transaction'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
END
$excalibase_session$;
`

// superuserPsqlCommand is psql as postgres in dbName with the session pinned.
// Each script is its own -c, so its own transaction, and the first failure
// stops the rest. serverVersionNum is the server_version_num the probe read,
// or 0 when it is unknown.
func superuserPsqlCommand(dbName string, serverVersionNum int, scripts ...string) []string {
	cmd := []string{"psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", superuserConninfo(dbName, serverVersionNum)}
	for _, script := range scripts {
		cmd = append(cmd, "-c", sessionGuardSQL+script)
	}
	return cmd
}

// serverVersionProbeCommand reads server_version_num with the connect-safe
// options, which never name a versioned setting, so it survives both an old
// server and an owner's poisoned preload library.
func serverVersionProbeCommand(dbName string) []string {
	return []string{"psql", "-X", "-tA", "-U", "postgres", "-d", superuserConninfo(dbName, 0), "-c", "SHOW server_version_num"}
}

func superuserConninfo(dbName string, serverVersionNum int) string {
	options := make([]string, 0, len(pinnedSessionOptions)+1)
	for _, option := range pinnedSessionOptions {
		options = append(options, "-c "+option)
	}
	if serverVersionNum >= scramIterationsMinVersion {
		// Fix the SCRAM work factor to the default so an owner cannot lower it.
		options = append(options, "-c scram_iterations=4096")
	}
	if serverVersionNum >= transactionTimeoutMinVersion {
		options = append(options, "-c transaction_timeout=0")
	}
	return "dbname=" + conninfoValue(dbName) + " options=" + conninfoValue(strings.Join(options, " "))
}

// conninfoValue quotes a libpq keyword value.
func conninfoValue(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return "'" + escaped + "'"
}

// terminateNonServiceBackendsCommand kills every client backend that is not a
// platform service — so the blocker is cleared whatever it holds: a lock on an
// excalibase object, a dependent object elsewhere, or a catalog row the second
// script needs (pg_blocking_pids could not see the last two). A customer can
// only be behind a non-service backend: customer SQL runs as excalibase_app,
// and the owner login is the owner's. Platform services (auth_admin,
// cdc_watcher, excalibase_scheduler, the DocumentDB roles, streaming_replica,
// postgres) are roles a customer can neither create nor assume, so they alone
// are spared, together with any superuser.
func terminateNonServiceBackendsCommand(dbName string) []string {
	return []string{"psql", "-X", "-U", "postgres", "-d", superuserConninfo(dbName, 0), "-c", terminateNonServiceBackendsSQL}
}

var terminateNonServiceBackendsSQL = `
SELECT pg_terminate_backend(a.pid)
FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND a.pid <> pg_backend_pid()
  AND a.backend_type = 'client backend'
  AND a.usename IS NOT NULL
  AND a.usename <> ALL (` + serviceRoleArray() + `)
  AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = a.usename AND r.rolsuper);
`

// serviceRoleArray is the SQL text[] of the platform service logins the
// terminate step must never kill. excalibase_app is NOT among them: it is the
// role customer SQL runs as, so during a rebuild a customer backend wears it.
// The owner login is not here either.
func serviceRoleArray() string {
	spared := make([]string, 0)
	for _, role := range pgroles.ReservedNames() {
		if role == roleApp {
			continue
		}
		spared = append(spared, role)
	}
	quoted := make([]string, 0, len(spared))
	for _, role := range spared {
		quoted = append(quoted, "'"+strings.ReplaceAll(role, "'", "''")+"'")
	}
	return "ARRAY[" + strings.Join(quoted, ", ") + "]::text[]"
}

// reservedSchemaFirstSQL takes the excalibase schema back before anything a
// customer can make fail: it runs as its own transaction ahead of the squat
// guard, so a project whose registration stops still loses every write path
// its app role had into the schema and still gets the CREATE EXTENSION guard.
func reservedSchemaFirstSQL() string {
	return reservedSchemaRebuildSQL + reservedSchemaLockdownSQL(roleApp) + reservedSchemaGuardSQL
}

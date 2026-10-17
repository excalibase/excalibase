package service

import (
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// The role SQL runs as the superuser in a database the customer owns, and an
// owner can attach settings to that database: read-only transactions, a role
// to switch to, timeouts, PL/pgSQL extra errors. Any of them would make the
// role SQL fail on every pass and leave a project on whatever grants it had
// (EXC-623). Client options outrank database settings, so the command pins
// every setting the SQL depends on.
var pinnedSessionOptions = []string{
	"role=none",
	"default_transaction_read_only=off",
	"statement_timeout=0",
	"lock_timeout=0",
	"idle_in_transaction_session_timeout=0",
	"idle_session_timeout=0",
	"exit_on_error=off",
	"standard_conforming_strings=on",
	"transform_null_equals=off",
	"plpgsql.extra_errors=none",
	"search_path=pg_catalog,pg_temp",
}

// transactionTimeoutMajor is the first Postgres major with transaction_timeout;
// an older server refuses the connection when a client names it.
const transactionTimeoutMajor = 17

// sessionGuardSQL starts every script: it refuses to go on unless the session
// is the superuser itself, not a role the session was switched to, and turns
// transaction_timeout off where the server has it and the client options
// could not name it.
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
// stops the rest.
func superuserPsqlCommand(dbName, postgresMajor string, scripts ...string) []string {
	cmd := []string{"psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", superuserConninfo(dbName, postgresMajor)}
	for _, script := range scripts {
		cmd = append(cmd, "-c", sessionGuardSQL+script)
	}
	return cmd
}

func superuserConninfo(dbName, postgresMajor string) string {
	options := make([]string, 0, len(pinnedSessionOptions)+1)
	for _, option := range pinnedSessionOptions {
		options = append(options, "-c "+option)
	}
	if major, err := strconv.Atoi(postgresMajor); err == nil && major >= transactionTimeoutMajor {
		options = append(options, "-c transaction_timeout=0")
	}
	return "dbname=" + conninfoValue(dbName) + " options=" + conninfoValue(strings.Join(options, " "))
}

// conninfoValue quotes a libpq keyword value.
func conninfoValue(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return "'" + escaped + "'"
}

// reservedSchemaFirstSQL takes the excalibase schema back before anything a
// customer can make fail: it runs as its own transaction ahead of the squat
// guard, so a project whose registration stops still loses every write path
// its app role had into the schema.
func reservedSchemaFirstSQL() string {
	return reservedSchemaRebuildSQL + reservedSchemaLockdownSQL(roleApp)
}

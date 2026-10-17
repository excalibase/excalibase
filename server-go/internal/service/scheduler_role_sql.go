package service

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/platformdb"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// roleScheduler is the login the function scheduler and the deploy-time cron
// sync write the excalibase schema's bookkeeping tables as (EXC-623). No
// customer path connects as it, it never writes a customer table, so no
// customer trigger runs as it, and it holds nothing but DML on those tables.
const roleScheduler = "excalibase_scheduler"

// BuildSchedulerRoleSQL creates excalibase_scheduler if absent and the
// scheduler tables, owned by the superuser that runs it, and grants the role
// DML on them and nothing else. Its search_path never reaches a schema a
// customer can create in. Run after BuildProjectRoleSQL, which leaves the
// schema the superuser's alone.
func BuildSchedulerRoleSQL(schedulerPass, dbName string) string {
	role := schema.QuoteIdent(roleScheduler)
	return fmt.Sprintf(`
DO $excalibase_scheduler_role$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'excalibase_scheduler') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %%L', %s);
  END IF;
END $excalibase_scheduler_role$;
ALTER ROLE %s IN DATABASE %s SET search_path = pg_catalog, pg_temp;
%s
ALTER TABLE excalibase.excalibase_scheduled_functions OWNER TO CURRENT_USER;
ALTER TABLE excalibase.excalibase_cron_jobs OWNER TO CURRENT_USER;
REVOKE ALL ON excalibase.excalibase_scheduled_functions, excalibase.excalibase_cron_jobs FROM PUBLIC;
GRANT USAGE ON SCHEMA excalibase TO %s;
GRANT SELECT, INSERT, UPDATE, DELETE ON excalibase.excalibase_scheduled_functions, excalibase.excalibase_cron_jobs TO %s;
`,
		role, sqlTextLiteral(schedulerPass),
		role, schema.QuoteIdent(dbName),
		platformdb.SchedulerTablesSQL,
		role, role,
	)
}

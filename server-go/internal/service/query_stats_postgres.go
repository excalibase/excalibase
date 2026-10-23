package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// pg_stat_statements is read as the superuser: a lesser role sees other
// roles' statements as "<insufficient privilege>". The superuser's own rows
// are filtered out with every platform role's, so nothing the platform ran
// (role passwords included) is ever answered.

// projectStatementRoleFilter keeps the statements of the project's roles: the
// owner's, every role the owner created, and excalibase_app, as which the API
// engine and Studio's SQL run. It refuses superusers, replication logins,
// Postgres' own roles and every other platform login.
var projectStatementRoleFilter = buildProjectStatementRoleFilter()

func buildProjectStatementRoleFilter() string {
	names := make([]string, 0, len(pgroles.ReservedNames()))
	for _, name := range pgroles.ReservedNames() {
		if name != roleApp {
			names = append(names, sqlQuote(name))
		}
	}
	prefixes := make([]string, 0, len(pgroles.ReservedPrefixes()))
	for _, prefix := range pgroles.ReservedPrefixes() {
		prefixes = append(prefixes, sqlQuote(prefix))
	}
	return "NOT r.rolsuper AND NOT r.rolreplication AND (r.rolname = " + sqlQuote(roleApp) + " OR NOT (" +
		"lower(r.rolname) = ANY (ARRAY[" + strings.Join(names, ", ") + "]::text[])" +
		" OR EXISTS (SELECT 1 FROM unnest(ARRAY[" + strings.Join(prefixes, ", ") + "]::text[]) AS p(prefix)" +
		" WHERE starts_with(lower(r.rolname), p.prefix))))"
}

func sqlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// statsSchemaLookup finds the extension's schema, and only while the library
// is preloaded: before that its views raise instead of answering.
const statsSchemaLookup = `SELECT n.nspname INTO stats_schema
  FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
  WHERE e.extname = 'pg_stat_statements'
    AND 'pg_stat_statements' = ANY (string_to_array(replace(current_setting('shared_preload_libraries'), ' ', ''), ','));
  IF stats_schema IS NULL THEN
    RETURN NULL;
  END IF;`

const projectDatabaseOID = `(SELECT d.oid FROM pg_database d WHERE d.datname = current_database())`

// The functions live in the session's pg_temp and go with it.
var queryStatsFunctionSQL = `SET statement_timeout = '10s';
CREATE FUNCTION pg_temp.excalibase_query_stats(sort_by text, max_rows integer) RETURNS text
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $excalibase_stats$
DECLARE
  stats_schema text;
  result text;
BEGIN
  ` + statsSchemaLookup + `
  EXECUTE format($q$SELECT coalesce(json_agg(json_build_object('query', top.query, 'calls', top.calls,
      'totalTimeMs', top.total_ms, 'meanTimeMs', top.mean_ms, 'rows', top.rows) ORDER BY top.sort_key DESC), '[]')::text
    FROM (
      SELECT grouped.*, CASE WHEN $1 = 'mean' THEN grouped.mean_ms ELSE grouped.total_ms END AS sort_key
      FROM (
        SELECT s.query, sum(s.calls) AS calls, sum(s.total_exec_time) AS total_ms,
          coalesce(sum(s.total_exec_time) / nullif(sum(s.calls), 0), 0) AS mean_ms, sum(s.rows) AS rows
        FROM %I.pg_stat_statements s JOIN pg_roles r ON r.oid = s.userid
        WHERE s.dbid = ` + projectDatabaseOID + ` AND ` + projectStatementRoleFilter + `
        GROUP BY s.query
      ) grouped
      ORDER BY sort_key DESC
      LIMIT $2
    ) top$q$, stats_schema) INTO result USING sort_by, max_rows;
  RETURN result;
END
$excalibase_stats$;
`

var resetQueryStatsSQL = `SET statement_timeout = '10s';
CREATE FUNCTION pg_temp.excalibase_reset_query_stats() RETURNS text
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $excalibase_stats$
DECLARE
  stats_schema text;
  project_role oid;
BEGIN
  ` + statsSchemaLookup + `
  FOR project_role IN SELECT r.oid FROM pg_roles r WHERE ` + projectStatementRoleFilter + ` LOOP
    EXECUTE format('SELECT %I.pg_stat_statements_reset($1, ` + projectDatabaseOID + `, 0)', stats_schema) USING project_role;
  END LOOP;
  RETURN 'reset';
END
$excalibase_stats$;
SELECT pg_temp.excalibase_reset_query_stats();
`

// queryStatsScript reads the project's statements; sort and limit are checked values.
func queryStatsScript(sort string, limit int) string {
	return queryStatsFunctionSQL + "SELECT pg_temp.excalibase_query_stats(" + sqlQuote(sort) + ", " + strconv.Itoa(limit) + ");\n"
}

// queryStatsPsql runs script as the superuser in the project database with
// the session pinned against the owner's database settings.
func queryStatsPsql(dbName, script string) []string {
	return []string{"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-U", "postgres",
		"-d", superuserConninfo(dbName, 0), "-c", script}
}

func (s *QueryStatsService) runPostgres(ctx context.Context, inst *domain.DatabaseInstance, script string) (string, error) {
	psql, err := s.databases.primaryPsql(ctx, inst)
	if err != nil {
		return "", err
	}
	out, err := psql(ctx, queryStatsPsql(inst.DatabaseName, script))
	if err != nil {
		return "", fmt.Errorf("read query statistics of %s: %w", inst.ProjectID, err)
	}
	return lastLine(out), nil
}

func (s *QueryStatsService) listPostgres(ctx context.Context, inst *domain.DatabaseInstance, sort string, limit int) (*SlowQueryReport, error) {
	answer, err := s.runPostgres(ctx, inst, queryStatsScript(sort, limit))
	if err != nil {
		return nil, err
	}
	report := emptyReport(engineNamePostgres, sort)
	if answer == "" {
		return report, nil
	}
	if err := json.Unmarshal([]byte(answer), &report.Queries); err != nil {
		return nil, fmt.Errorf("read query statistics of %s: unexpected answer", inst.ProjectID)
	}
	report.Enabled = true
	return report, nil
}

func (s *QueryStatsService) resetPostgres(ctx context.Context, inst *domain.DatabaseInstance) error {
	answer, err := s.runPostgres(ctx, inst, resetQueryStatsSQL)
	if err != nil {
		return err
	}
	if answer != "reset" {
		return ErrQueryStatsOff
	}
	return nil
}

// lastLine is psql's answer: notices on stderr come before it.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

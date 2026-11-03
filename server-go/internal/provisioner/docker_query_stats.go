package provisioner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// Slow queries on a single host (EXC-673): pg_stat_statements as Kubernetes
// runs it (EXC-654), turned on with ALTER SYSTEM so the settings live in the
// data directory and survive pause, resume and restores, which recreate the
// container. The extension lives in the maintenance database, in a schema only
// the superuser owns, and every statement runs there with the session pinned:
// the project database is the owner's, who could attach settings to it.

const queryStatsLibrary = "pg_stat_statements"

// QueryStatsSchema holds the extension in the maintenance database.
const QueryStatsSchema = "excalibase_stats"

type queryStatsSetting struct{ name, value string }

// dockerQueryStatsSettings match k8s.queryStatsParameters; utility statements
// stay untracked so an ALTER ROLE ... PASSWORD is never kept verbatim.
var dockerQueryStatsSettings = []queryStatsSetting{
	{"pg_stat_statements.max", "5000"},
	{"pg_stat_statements.track", "top"},
	{"pg_stat_statements.track_utility", "off"},
}

// maintenanceConninfo is the superuser's session in the maintenance database
// with every setting an owner could poison pinned (EXC-623, EXC-670).
const maintenanceConninfo = "dbname=postgres options='-c search_path=pg_catalog,pg_temp -c role=none " +
	"-c local_preload_libraries= -c session_preload_libraries= -c default_transaction_read_only=off " +
	"-c statement_timeout=30s -c lock_timeout=10s'"

// preloadValue is what shared_preload_libraries may hold for it to be rewritten.
var preloadValue = regexp.MustCompile(`^[A-Za-z0-9_$,. -]*$`)

var (
	errNoStatsContainer  = errors.New("query statistics: container id missing")
	errUnexpectedPreload = errors.New("shared_preload_libraries holds an unexpected value; statistics left off")
)

// readyPoll is how often the restarted server is asked for an answer.
const readyPoll = 500 * time.Millisecond

// queryStatsExtensionSQL makes the stats schema the superuser's own, closed to
// everyone else, and the extension in it.
var queryStatsExtensionSQL = pgroles.PinnedSearchPath + `DO $excalibase_stats$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = '` + QueryStatsSchema + `') THEN
    CREATE SCHEMA ` + QueryStatsSchema + `;
  ELSIF NOT (SELECT r.rolsuper FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid = n.nspowner
             WHERE n.nspname = '` + QueryStatsSchema + `') THEN
    RAISE EXCEPTION 'schema ` + QueryStatsSchema + ` is not owned by a superuser';
  END IF;
END
$excalibase_stats$;
REVOKE ALL ON SCHEMA ` + QueryStatsSchema + ` FROM PUBLIC;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements SCHEMA ` + QueryStatsSchema + `;
`

// SetQueryStats decides whether new projects carry pg_stat_statements.
func (p *DockerPostgreSQLProvisioner) SetQueryStats(on bool) { p.queryStats = on }

// QueryStatsOn reports whether new projects carry pg_stat_statements.
func (p *DockerPostgreSQLProvisioner) QueryStatsOn() bool { return p.queryStats }

// EnableQueryStats turns statement statistics on in a running project
// container: it preloads the library and restarts the container once if the
// library is not loaded yet, then makes sure of the extension and the
// settings. Postgres 14 refuses the library's settings until it is loaded, so
// they follow the restart, and whatever was counted while utility statements
// were tracked is cleared with them.
func (p *DockerPostgreSQLProvisioner) EnableQueryStats(ctx context.Context, containerID string) error {
	if containerID == "" {
		return errNoStatsContainer
	}
	preloaded, err := p.preloadedLibraries(ctx, containerID)
	if err != nil {
		return err
	}
	restarted := !slices.Contains(preloaded, queryStatsLibrary)
	if restarted {
		if err := p.preloadQueryStats(ctx, containerID, append(preloaded, queryStatsLibrary)); err != nil {
			return err
		}
	}
	if _, err := p.maintenancePsql(ctx, containerID, queryStatsExtensionSQL); err != nil {
		return fmt.Errorf("create the statistics extension: %w", err)
	}
	utilityTracked := restarted
	if !restarted {
		out, err := p.maintenancePsql(ctx, containerID, "SHOW pg_stat_statements.track_utility;\n")
		if err != nil {
			return fmt.Errorf("read the statistics settings: %w", err)
		}
		utilityTracked = lastLine(out) != "off"
	}
	if _, err := p.maintenancePsql(ctx, containerID, queryStatsSettingsSQL(utilityTracked)); err != nil {
		return fmt.Errorf("write the statistics settings: %w", err)
	}
	return nil
}

// queryStatsSettingsSQL writes the settings and, while utility statements were
// tracked, clears the statistics once every session has reloaded them.
func queryStatsSettingsSQL(clear bool) string {
	var script strings.Builder
	for _, setting := range dockerQueryStatsSettings {
		script.WriteString("ALTER SYSTEM SET " + setting.name + " = " + quoteSQLString(setting.value) + ";\n")
	}
	script.WriteString("SELECT pg_catalog.pg_reload_conf();\n")
	if clear {
		script.WriteString("SELECT pg_catalog.pg_sleep(1);\nSELECT " + QueryStatsSchema + ".pg_stat_statements_reset();\n")
	}
	return script.String()
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (p *DockerPostgreSQLProvisioner) preloadedLibraries(ctx context.Context, containerID string) ([]string, error) {
	out, err := p.maintenancePsql(ctx, containerID, "SHOW shared_preload_libraries;\n")
	if err != nil {
		return nil, fmt.Errorf("read shared_preload_libraries: %w", err)
	}
	// Empty output is an empty list: nothing preloaded.
	value := lastLine(out)
	if !preloadValue.MatchString(value) {
		return nil, errUnexpectedPreload
	}
	var libraries []string
	for _, library := range strings.Split(value, ",") {
		if library = strings.TrimSpace(library); library != "" {
			libraries = append(libraries, library)
		}
	}
	return libraries, nil
}

// preloadQueryStats preloads the library and restarts the container: it
// loads at server start only. Each library is its own list item.
func (p *DockerPostgreSQLProvisioner) preloadQueryStats(ctx context.Context, containerID string, libraries []string) error {
	items := make([]string, 0, len(libraries))
	for _, library := range libraries {
		items = append(items, quoteSQLString(library))
	}
	script := "ALTER SYSTEM SET shared_preload_libraries = " + strings.Join(items, ", ") + ";\n"
	if _, err := p.maintenancePsql(ctx, containerID, script); err != nil {
		return fmt.Errorf("preload pg_stat_statements: %w", err)
	}
	if err := p.docker.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("stop to load pg_stat_statements: %w", err)
	}
	if err := p.StartHoldingPort(ctx, containerID); err != nil {
		return fmt.Errorf("start to load pg_stat_statements (the database is stopped): %w", err)
	}
	return p.waitForMaintenance(ctx, containerID)
}

// waitForMaintenance waits for the restarted server to answer in the pinned
// maintenance session, so even the probe runs nothing an owner set.
func (p *DockerPostgreSQLProvisioner) waitForMaintenance(ctx context.Context, containerID string) error {
	deadline := time.Now().Add(postgresReadyTimeout)
	for {
		_, err := p.maintenancePsql(ctx, containerID, "SELECT 1;\n")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres did not answer within %s after loading pg_stat_statements: %w", postgresReadyTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// maintenancePsql runs script on stdin as the superuser in the pinned
// maintenance session. Outside a -c string each statement is its own
// transaction, as ALTER SYSTEM needs.
func (p *DockerPostgreSQLProvisioner) maintenancePsql(ctx context.Context, containerID, script string) (string, error) {
	cmd := []string{"psql", "-X", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-U", defaultPostgresSuperuser, "-d", maintenanceConninfo, "-f", "-"}
	return p.docker.ExecInContainerStdin(ctx, containerID, cmd, script)
}

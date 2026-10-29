//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-673: on a real engine, a single host's project gets pg_stat_statements
// in its maintenance database, created with the feature on or turned on later,
// and the owner can neither reach nor plant anything in the stats schema.

type statsProject struct {
	docker    *RealDockerClient
	container string
	owner     string
	password  string
}

func (s statsProject) superuser(t *testing.T, database, sql string) string {
	t.Helper()
	out, err := s.docker.ExecInContainerStdin(context.Background(), s.container,
		[]string{"psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", database, "-f", "-"}, sql)
	if err != nil {
		t.Fatalf("superuser in %s: %s: %v", database, sql, err)
	}
	return strings.TrimSpace(out)
}

// asOwner runs sql as the project's owner over TCP, as a customer would.
func (s statsProject) asOwner(database, sql string) (string, error) {
	conninfo := fmt.Sprintf("host=127.0.0.1 dbname=%s user=%s password=%s", database, s.owner, s.password)
	return s.docker.ExecInContainerStdin(context.Background(), s.container,
		[]string{"psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-d", conninfo, "-f", "-"}, sql)
}

func provisionStatsProject(t *testing.T, major string, queryStats bool) (statsProject, *DockerPostgreSQLProvisioner) {
	t.Helper()
	docker := requireDocker(t)
	t.Cleanup(func() { docker.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	project := fmt.Sprintf("it-stats-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerPostgreSQLProvisioner(docker)
	p.SetQueryStats(queryStats)
	req := domain.ProvisioningRequest{ProjectName: project, DBType: domain.PostgreSQL, PostgresVersion: major}
	result, err := p.Provision(ctx, req, testTier, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), DatabaseContainerName(project), project) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	return statsProject{docker: docker, container: result.Namespace, owner: result.Username, password: result.Password}, p
}

func assertStatisticsOn(t *testing.T, project statsProject) {
	t.Helper()
	if got := project.superuser(t, "postgres", "SHOW shared_preload_libraries;"); !strings.Contains(got, "pg_stat_statements") {
		t.Fatalf("shared_preload_libraries = %q", got)
	}
	if got := project.superuser(t, "postgres", "SHOW pg_stat_statements.track_utility;"); got != "off" {
		t.Errorf("track_utility = %q", got)
	}
	where := project.superuser(t, "postgres", `SELECT n.nspname || ':' || r.rolsuper FROM pg_extension e
JOIN pg_namespace n ON n.oid = e.extnamespace JOIN pg_roles r ON r.oid = n.nspowner WHERE e.extname = 'pg_stat_statements';`)
	if where != QueryStatsSchema+":true" {
		t.Fatalf("extension in %q, want the superuser's %s", where, QueryStatsSchema)
	}
	if got := project.superuser(t, "app", "SELECT count(*) FROM pg_extension WHERE extname = 'pg_stat_statements';"); got != "0" {
		t.Errorf("the extension is in the project database too: %s", got)
	}
	// The owner's statement in its own database is counted, and seen from the maintenance database.
	if _, err := project.asOwner("app", "CREATE TABLE orders (id int); SELECT count(*) AS owner_marker FROM orders;"); err != nil {
		t.Fatalf("owner workload: %v", err)
	}
	seen := project.superuser(t, "postgres", `SELECT count(*) FROM `+QueryStatsSchema+`.pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid WHERE d.datname = 'app' AND s.query LIKE '%owner_marker%';`)
	if seen != "1" {
		t.Errorf("owner statement seen %s times", seen)
	}
	// The owner reads nothing there and plants nothing there.
	if _, err := project.asOwner("postgres", "CREATE TABLE "+QueryStatsSchema+".planted (id int);"); err == nil {
		t.Error("the owner created a table in the stats schema")
	}
	if _, err := project.asOwner("postgres", "SELECT count(*) FROM "+QueryStatsSchema+".pg_stat_statements;"); err == nil {
		t.Error("the owner read the statistics view")
	}
}

func TestDockerQueryStatsAtCreationOnARealEngine(t *testing.T) {
	for _, major := range []string{"14", "17"} {
		t.Run("postgres "+major, func(t *testing.T) {
			project, _ := provisionStatsProject(t, major, true)
			assertStatisticsOn(t, project)
		})
	}
}

// A project created without them turns them on with one restart; its data,
// and a poisoned setting the owner attached to its database, change nothing.
func TestDockerQueryStatsEnabledLaterOnARealEngine(t *testing.T) {
	project, p := provisionStatsProject(t, "17", false)
	if got := project.superuser(t, "postgres", "SHOW shared_preload_libraries;"); strings.Contains(got, "pg_stat_statements") {
		t.Fatalf("created with statistics: %q", got)
	}
	if _, err := project.asOwner("app", `CREATE TABLE kept (id int); INSERT INTO kept VALUES (7);
ALTER DATABASE app SET search_path = public, pg_catalog; ALTER DATABASE app SET statement_timeout = '1ms';`); err != nil {
		t.Fatalf("owner setup: %v", err)
	}
	if err := p.EnableQueryStats(context.Background(), project.container); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	if got := project.superuser(t, "app", "SET statement_timeout = 0; SELECT id FROM kept;"); !strings.HasSuffix(got, "7") {
		t.Errorf("data after the restart = %q", got)
	}
	if _, err := project.asOwner("app", "ALTER DATABASE app RESET ALL;"); err != nil {
		t.Fatalf("owner reset: %v", err)
	}
	assertStatisticsOn(t, project)
	if err := p.EnableQueryStats(context.Background(), project.container); err != nil {
		t.Fatalf("second EnableQueryStats: %v", err)
	}
}

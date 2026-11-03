package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// statsDocker answers the preload read with preload and records every stdin script.
type statsDocker struct {
	*mockDockerClient
	preload      string
	trackUtility string
	cmds         [][]string
	scripts      []string
}

func newStatsDocker(preload string) *statsDocker {
	return &statsDocker{mockDockerClient: newMockDocker(), preload: preload, trackUtility: "off"}
}

func (s *statsDocker) ExecInContainerStdin(_ context.Context, _ string, cmd []string, stdin string) (string, error) {
	s.cmds = append(s.cmds, cmd)
	s.scripts = append(s.scripts, stdin)
	if strings.Contains(stdin, "SHOW shared_preload_libraries") {
		return s.preload + "\n", nil
	}
	if strings.Contains(stdin, "SHOW pg_stat_statements.track_utility") {
		return s.trackUtility + "\n", nil
	}
	return "", nil
}

func (s *statsDocker) scriptIndex(containing string) int {
	for i, script := range s.scripts {
		if strings.Contains(script, containing) {
			return i
		}
	}
	return -1
}

func (s *statsDocker) script(containing string) (string, bool) {
	for _, script := range s.scripts {
		if strings.Contains(script, containing) {
			return script, true
		}
	}
	return "", false
}

// EXC-673: the settings match what CloudNativePG renders on Kubernetes (EXC-654).
func TestDockerQueryStatsSettingsMatchKubernetes(t *testing.T) {
	want := map[string]string{
		"pg_stat_statements.max":           "5000",
		"pg_stat_statements.track":         "top",
		"pg_stat_statements.track_utility": "off",
	}
	if len(dockerQueryStatsSettings) != len(want) {
		t.Fatalf("settings = %v", dockerQueryStatsSettings)
	}
	for _, setting := range dockerQueryStatsSettings {
		if want[setting.name] != setting.value {
			t.Errorf("%s = %q, want %q", setting.name, setting.value, want[setting.name])
		}
	}
}

func TestEnableQueryStatsPreloadsTheLibraryRestartsAndCreatesTheExtension(t *testing.T) {
	docker := newStatsDocker("")
	docker.containers["db-1"] = containerRunning
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.EnableQueryStats(context.Background(), "db-1"); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	alter, ok := docker.script("ALTER SYSTEM SET shared_preload_libraries")
	if !ok {
		t.Fatalf("no ALTER SYSTEM script; scripts: %q", docker.scripts)
	}
	if !strings.Contains(alter, "ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements';") {
		t.Errorf("preload script:\n%s", alter)
	}
	// Postgres 14 refuses the library's settings until it is loaded: they come after the restart.
	if strings.Contains(alter, "pg_stat_statements.") {
		t.Errorf("library settings written before the library loads:\n%s", alter)
	}
	settings, ok := docker.script("ALTER SYSTEM SET pg_stat_statements.track_utility")
	if !ok {
		t.Fatalf("no settings script; scripts: %q", docker.scripts)
	}
	for _, line := range []string{
		"ALTER SYSTEM SET pg_stat_statements.max = '5000';",
		"ALTER SYSTEM SET pg_stat_statements.track = 'top';",
		"ALTER SYSTEM SET pg_stat_statements.track_utility = 'off';",
		"SELECT pg_catalog.pg_reload_conf();",
	} {
		if !strings.Contains(settings, line) {
			t.Errorf("settings script lacks %q:\n%s", line, settings)
		}
	}
	// What was counted before track_utility was off (an ALTER ROLE ... PASSWORD) is dropped.
	reset, ok := docker.script("pg_stat_statements_reset()")
	if !ok || !strings.Contains(reset, "excalibase_stats.pg_stat_statements_reset()") {
		t.Errorf("no reset after the first load; scripts: %q", docker.scripts)
	}
	if docker.stops != 1 || docker.starts != 1 {
		t.Errorf("restart: stops=%d starts=%d, want 1 and 1", docker.stops, docker.starts)
	}
	extension, ok := docker.script("CREATE EXTENSION IF NOT EXISTS pg_stat_statements SCHEMA excalibase_stats")
	if !ok {
		t.Fatalf("no extension script; scripts: %q", docker.scripts)
	}
	if !strings.Contains(extension, "REVOKE ALL ON SCHEMA excalibase_stats FROM PUBLIC") {
		t.Errorf("stats schema not closed to PUBLIC:\n%s", extension)
	}
	if !strings.Contains(extension, "rolsuper") {
		t.Errorf("an existing stats schema is not checked to be the superuser's:\n%s", extension)
	}
}

// Every statement runs in the maintenance database with the session pinned,
// never in the project database whose owner could attach settings to it.
func TestEnableQueryStatsRunsOnlyInThePinnedMaintenanceDatabase(t *testing.T) {
	docker := newStatsDocker("")
	docker.containers["db-1"] = containerRunning
	p := NewDockerPostgreSQLProvisioner(docker)
	if err := p.EnableQueryStats(context.Background(), "db-1"); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	if len(docker.cmds) == 0 {
		t.Fatal("no psql ran")
	}
	for _, cmd := range docker.cmds {
		joined := strings.Join(cmd, " ")
		for _, want := range []string{"dbname=postgres", "search_path=pg_catalog,pg_temp", "role=none",
			"local_preload_libraries=", "session_preload_libraries=", "-U postgres", "ON_ERROR_STOP=1"} {
			if !strings.Contains(joined, want) {
				t.Errorf("psql %q lacks %q", joined, want)
			}
		}
	}
	// The readiness wait after the restart runs no SQL outside the pinned session.
	for _, cmd := range docker.execLog {
		if len(cmd) > 0 && cmd[0] == "psql" {
			t.Errorf("unpinned psql %q", cmd)
		}
	}
}

func TestEnableQueryStatsKeepsLibrariesAlreadyPreloaded(t *testing.T) {
	docker := newStatsDocker("auto_explain, pg_cron")
	docker.containers["db-1"] = containerRunning
	if err := NewDockerPostgreSQLProvisioner(docker).EnableQueryStats(context.Background(), "db-1"); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	alter, _ := docker.script("ALTER SYSTEM SET shared_preload_libraries")
	if !strings.Contains(alter, "shared_preload_libraries = 'auto_explain', 'pg_cron', 'pg_stat_statements';") {
		t.Errorf("preload not merged:\n%s", alter)
	}
}

func TestEnableQueryStatsOnALoadedLibraryNeedsNoRestart(t *testing.T) {
	docker := newStatsDocker("pg_stat_statements")
	docker.containers["db-1"] = containerRunning
	if err := NewDockerPostgreSQLProvisioner(docker).EnableQueryStats(context.Background(), "db-1"); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	if _, preloaded := docker.script("shared_preload_libraries ="); preloaded || docker.stops != 0 {
		t.Errorf("already loaded: preload rewritten=%v stops=%d", preloaded, docker.stops)
	}
	if _, reset := docker.script("pg_stat_statements_reset()"); reset {
		t.Error("a loaded library's statistics were reset")
	}
	if _, ok := docker.script("ALTER SYSTEM SET pg_stat_statements.track_utility = 'off'"); !ok {
		t.Error("the settings are still made sure of")
	}
	if _, ok := docker.script("CREATE EXTENSION IF NOT EXISTS pg_stat_statements"); !ok {
		t.Error("the extension is still made sure of")
	}
}

func TestEnableQueryStatsRefusesAnUnexpectedPreloadValue(t *testing.T) {
	docker := newStatsDocker("evil'; DROP TABLE x; --")
	docker.containers["db-1"] = containerRunning
	err := NewDockerPostgreSQLProvisioner(docker).EnableQueryStats(context.Background(), "db-1")
	if err == nil {
		t.Fatal("an unexpected shared_preload_libraries value was accepted")
	}
	if _, altered := docker.script("ALTER SYSTEM"); altered {
		t.Error("ALTER SYSTEM ran after a refused value")
	}
}

func TestEnableQueryStatsNeedsAContainer(t *testing.T) {
	if err := NewDockerPostgreSQLProvisioner(newStatsDocker("")).EnableQueryStats(context.Background(), ""); err == nil {
		t.Error("an empty container id was accepted")
	}
}

func TestProvisionTurnsQueryStatsOnOnlyWithTheFeature(t *testing.T) {
	for _, on := range []bool{true, false} {
		docker := newStatsDocker("")
		p := NewDockerPostgreSQLProvisioner(docker)
		p.SetQueryStats(on)
		req := domain.ProvisioningRequest{ProjectName: "stats-app", DBType: domain.PostgreSQL, PostgresVersion: "17"}
		if _, err := p.Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {}); err != nil {
			t.Fatalf("Provision(queryStats=%v): %v", on, err)
		}
		_, created := docker.script("CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
		if created != on {
			t.Errorf("queryStats=%v: extension created = %v", on, created)
		}
	}
}

// The statistics are on before the owner exists, so nothing of the customer's
// is in the container when the superuser restarts it.
func TestProvisionTurnsQueryStatsOnBeforeTheOwnerExists(t *testing.T) {
	docker := newStatsDocker("")
	p := NewDockerPostgreSQLProvisioner(docker)
	p.SetQueryStats(true)
	req := domain.ProvisioningRequest{ProjectName: "stats-app", DBType: domain.PostgreSQL, PostgresVersion: "17"}
	if _, err := p.Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	stats, owner := -1, -1
	for i, script := range docker.scripts {
		if stats < 0 && strings.Contains(script, "pg_stat_statements") {
			stats = i
		}
		if owner < 0 && strings.Contains(script, "CREATE ROLE") {
			owner = i
		}
	}
	if stats < 0 || owner < 0 || stats > owner {
		t.Errorf("statistics script %d must come before the owner's %d", stats, owner)
	}
}

// A failure after the restart still clears what was counted while utility
// statements (an ALTER ROLE ... PASSWORD) were tracked; a retry clears it too.
func TestEnableQueryStatsClearsUtilityStatementsUntilTheSettingsHold(t *testing.T) {
	docker := newStatsDocker("pg_stat_statements")
	docker.trackUtility = "on"
	docker.containers["db-1"] = containerRunning
	if err := NewDockerPostgreSQLProvisioner(docker).EnableQueryStats(context.Background(), "db-1"); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	settings, _ := docker.script("track_utility = 'off'")
	if !strings.Contains(settings, "pg_stat_statements_reset()") ||
		strings.Index(settings, "pg_reload_conf") > strings.Index(settings, "pg_stat_statements_reset()") {
		t.Errorf("settings not followed by a reset in the same script:\n%s", settings)
	}
	extension, _ := docker.script("CREATE EXTENSION")
	if strings.Contains(extension, "ALTER SYSTEM") {
		t.Error("extension and settings share a script")
	}
	if docker.scriptIndex("CREATE EXTENSION") > docker.scriptIndex("track_utility = 'off'") {
		t.Error("the reset needs the extension first")
	}
}

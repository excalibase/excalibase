package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// Slow queries on a single host (EXC-673): the project's own container gets
// pg_stat_statements, and every read runs in its maintenance database.

func singleHostStats(t *testing.T, inst *domain.DatabaseInstance) (*registrationHarness, *recordingDocker) {
	t.Helper()
	h := newRegistrationHarness(t)
	docker := newRecordingDocker()
	h.svc.factory = provisioner.NewFactory(provisioner.NewDockerPostgreSQLProvisioner(docker))
	h.svc.dockerClient = docker
	if err := h.store.Create(inst); err != nil {
		t.Fatal(err)
	}
	return h, docker
}

func activeSingleHostProject() *domain.DatabaseInstance {
	inst := singleHostProject()
	inst.Status = "ACTIVE"
	inst.DatabaseName = "app"
	return inst
}

func TestEnableQueryStatsOnASingleHostTurnsThemOnInTheProjectsContainer(t *testing.T) {
	inst := activeSingleHostProject()
	h, docker := singleHostStats(t, inst)
	if err := h.svc.EnableQueryStats(context.Background(), inst.ProjectID); err != nil {
		t.Fatalf("EnableQueryStats: %v", err)
	}
	var created bool
	for _, statement := range docker.stdin {
		if strings.Contains(string(statement.body), "CREATE EXTENSION IF NOT EXISTS pg_stat_statements") {
			created = statement.container == inst.Namespace
		}
	}
	if !created {
		t.Fatalf("no extension created in %s: %v", inst.Namespace, docker.calls)
	}
}

func TestEnableQueryStatsOnASingleHostDocumentDBIsRefused(t *testing.T) {
	inst := activeSingleHostProject()
	inst.DocumentDB = true
	h, docker := singleHostStats(t, inst)
	if err := h.svc.EnableQueryStats(context.Background(), inst.ProjectID); !errors.Is(err, ErrQueryStatsDocumentDB) {
		t.Fatalf("err = %v", err)
	}
	if len(docker.stdin) != 0 {
		t.Errorf("ran %v", docker.calls)
	}
}

func TestEnableQueryStatsOnASingleHostHoldsTheProjectLease(t *testing.T) {
	inst := activeSingleHostProject()
	inst.Status = "PAUSED"
	h, docker := singleHostStats(t, inst)
	if err := h.svc.EnableQueryStats(context.Background(), inst.ProjectID); err == nil {
		t.Fatal("a paused project was restarted")
	}
	if len(docker.calls) != 0 {
		t.Errorf("ran %v", docker.calls)
	}
}

func dockerStatsProject() *domain.DatabaseInstance {
	inst := pgStatsProject()
	inst.DeploymentMode = domain.ModeDocker
	return inst
}

// The read never connects to the project database: it asks the maintenance
// database for the statements of the project database, by name.
func TestQueryStats_SingleHostReadsInTheMaintenanceDatabase(t *testing.T) {
	dbs := &fakeStatsDatabases{output: statsAnswer(`[]`)}
	svc := NewQueryStatsService(statsProjects(dockerStatsProject()), dbs, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 10); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range dbs.commands {
		joined := strings.Join(cmd, " ")
		if !strings.Contains(joined, "dbname='postgres'") || strings.Contains(joined, "appdb") && !strings.Contains(joined, "datname") {
			t.Errorf("psql outside the maintenance database: %s", joined)
		}
	}
	script := dbs.lastSQL(t)
	if strings.Contains(script, "current_database()") {
		t.Error("the single-host read filters by the maintenance database")
	}
	if !strings.Contains(script, "d.datname = 'appdb'") {
		t.Errorf("the read does not name the project database:\n%s", script)
	}
	if !strings.HasPrefix(script, sessionGuardSQL) || !strings.Contains(script, projectStatementRoleFilter) {
		t.Error("the read lost the session guard or the role filter")
	}
}

func TestQueryStats_SingleHostResetClearsTheProjectDatabaseFromTheMaintenanceDatabase(t *testing.T) {
	dbs := &fakeStatsDatabases{output: "reset\n"}
	svc := NewQueryStatsService(statsProjects(dockerStatsProject()), dbs, nil, features.NewStatic())
	if err := svc.Reset(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	script := dbs.lastSQL(t)
	if !strings.Contains(script, "pg_stat_statements_reset") || !strings.Contains(script, "d.datname = 'appdb'") ||
		strings.Contains(script, "current_database()") {
		t.Errorf("reset script = %s", script)
	}
	if cmd := strings.Join(dbs.commands[len(dbs.commands)-1], " "); !strings.Contains(cmd, "dbname='postgres'") {
		t.Errorf("reset ran outside the maintenance database: %s", cmd)
	}
}

// A project database name is quoted, never spliced.
func TestQueryStats_SingleHostQuotesTheDatabaseName(t *testing.T) {
	inst := dockerStatsProject()
	inst.DatabaseName = "app'db"
	dbs := &fakeStatsDatabases{output: statsAnswer(`[]`)}
	svc := NewQueryStatsService(statsProjects(inst), dbs, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 10); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dbs.lastSQL(t), "d.datname = 'app''db'") {
		t.Errorf("name not quoted:\n%s", dbs.lastSQL(t))
	}
}

func TestQueryStats_KubernetesStillReadsInTheProjectDatabase(t *testing.T) {
	dbs := &fakeStatsDatabases{output: statsAnswer(`[]`)}
	svc := NewQueryStatsService(statsProjects(pgStatsProject()), dbs, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 10); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dbs.lastSQL(t), "current_database()") {
		t.Error("kubernetes read changed")
	}
}

// A single-host DocumentDB project's documents share the maintenance database:
// no route reads or clears statistics there.
func TestQueryStats_SingleHostDocumentDBIsRefusedOnEveryRoute(t *testing.T) {
	inst := dockerStatsProject()
	inst.DocumentDB = true
	dbs := &fakeStatsDatabases{output: statsAnswer(`[]`)}
	svc := NewQueryStatsService(statsProjects(inst), dbs, nil, features.NewStatic())
	if _, err := svc.List(context.Background(), "p1", "total", 10); !errors.Is(err, ErrQueryStatsDocumentDB) {
		t.Errorf("list err = %v", err)
	}
	if err := svc.Reset(context.Background(), "p1"); !errors.Is(err, ErrQueryStatsDocumentDB) {
		t.Errorf("reset err = %v", err)
	}
	if err := svc.Enable(context.Background(), "p1"); !errors.Is(err, ErrQueryStatsDocumentDB) {
		t.Errorf("enable err = %v", err)
	}
	if len(dbs.commands) != 0 || len(dbs.enabled) != 0 {
		t.Errorf("ran %v / enabled %v", dbs.commands, dbs.enabled)
	}
}

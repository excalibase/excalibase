package hostmonitor

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeExec struct {
	output string
	err    error
	got    []string
	stdin  string
	target string
}

func (f *fakeExec) ExecInContainerStdin(_ context.Context, containerID string, cmd []string, stdin string) (string, error) {
	f.target, f.got, f.stdin = containerID, cmd, stdin
	return f.output, f.err
}

func sampledMonitor(t *testing.T) *Monitor {
	t.Helper()
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1e9, 1)
	monitor.Sample(context.Background())
	*now = now.Add(7 * time.Second)
	setStats(engine, *now, 1e9+7e9, 1)
	monitor.Sample(context.Background())
	return monitor
}

func TestDatabaseFiguresJoinTheContainerAndPostgres(t *testing.T) {
	monitor := sampledMonitor(t)
	exec := &fakeExec{output: "7|5|100|2147483648\n"}
	databases := NewDatabases(monitor, exec)
	inst := project()[0]
	inst.DatabaseName = "shop"
	figures, err := databases.DatabaseFigures(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if !figures.Running || figures.CPUCores == nil || *figures.CPUCores != 1 || *figures.MemoryBytes != 100<<20 {
		t.Errorf("container figures = %+v", figures)
	}
	if figures.Connections == nil || *figures.Connections != (service.DatabaseConnections{Total: 7, Idle: 5, Max: 100}) || *figures.SizeBytes != 2147483648 {
		t.Errorf("postgres figures = %+v %v", figures.Connections, figures.SizeBytes)
	}
	if exec.target != "db-id" || !slices.Contains(exec.got, "db=shop") || exec.got[0] != "psql" || slices.Contains(exec.got, "sh") {
		t.Errorf("exec = %s %v; psql with the database as a variable, no shell", exec.target, exec.got)
	}
}

// The probe runs as the superuser: never in the customer's database, whose
// owner could attach settings to it, with every name it reads from pg_catalog
// and the search path pinned, so an owner's function or view never runs (EXC-623).
func TestThePostgresProbeCannotBeHijackedByTheOwner(t *testing.T) {
	monitor := sampledMonitor(t)
	for _, documentDB := range []bool{false, true} {
		exec := &fakeExec{output: "1|1|100|10"}
		inst := project()[0]
		inst.DatabaseName, inst.DocumentDB = "shop", documentDB
		if _, err := NewDatabases(monitor, exec).DatabaseFigures(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
		conninfo := exec.got[1]
		for _, pinned := range []string{"dbname=postgres ", "search_path=pg_catalog,pg_temp", "role=none", "local_preload_libraries=", "session_preload_libraries=", "statement_timeout=5s"} {
			if !strings.Contains(conninfo, pinned) {
				t.Errorf("conninfo %q does not pin %s", conninfo, pinned)
			}
		}
		if slices.Contains(exec.got, "-c") || !strings.Contains(exec.stdin, ":'db'") {
			t.Errorf("the query goes on stdin with the database as a quoted variable: %v %q", exec.got, exec.stdin)
		}
		bare := regexp.MustCompile(`(^|[^.\w])(count|current_setting|pg_database_size|pg_stat_activity)\b`)
		if found := bare.FindString(exec.stdin); found != "" {
			t.Errorf("unqualified name %q in %q", found, exec.stdin)
		}
	}
}

func TestADocumentDBProjectCountsItsDocuments(t *testing.T) {
	monitor := sampledMonitor(t)
	exec := &fakeExec{output: "1|1|100|10"}
	inst := project()[0]
	inst.DocumentDB = true
	if _, err := NewDatabases(monitor, exec).DatabaseFigures(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if exec.stdin != documentDBFiguresSQL {
		t.Errorf("a DocumentDB project's size includes the postgres database: %v", exec.got)
	}
}

func TestAPostgresThatDoesNotAnswerLeavesTheContainerFigures(t *testing.T) {
	monitor := sampledMonitor(t)
	for _, exec := range []*fakeExec{{err: errors.New("exit 2")}, {output: "garbage"}, {output: "1|x|3|4"}} {
		figures, err := NewDatabases(monitor, exec).DatabaseFigures(context.Background(), project()[0])
		if err != nil || !figures.Running || figures.Connections != nil || figures.SizeBytes != nil || figures.MemoryBytes == nil {
			t.Errorf("%+v: figures = %+v, %v", exec, figures, err)
		}
	}
}

func TestAMySQLDatabaseHasContainerFiguresOnly(t *testing.T) {
	monitor := sampledMonitor(t)
	exec := &fakeExec{}
	inst := project()[0]
	inst.DBType = domain.MySQL
	figures, err := NewDatabases(monitor, exec).DatabaseFigures(context.Background(), inst)
	if err != nil || !figures.Running || exec.got != nil {
		t.Errorf("mysql = %+v %v, exec %v", figures, err, exec.got)
	}
}

func TestANotYetSampledDatabaseIsAskedTheEngine(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, _ := newTestMonitor(t, engine, store, project())
	engine.managed[0].State = "exited"
	figures, err := NewDatabases(monitor, &fakeExec{}).DatabaseFigures(context.Background(), project()[0])
	if err != nil || figures.Running {
		t.Errorf("a stopped database before the first sample = %+v %v", figures, err)
	}
	gone := project()[0]
	gone.Namespace = "no-such-container"
	figures, err = NewDatabases(monitor, &fakeExec{}).DatabaseFigures(context.Background(), gone)
	if err != nil || figures.Running {
		t.Errorf("a database whose container is gone = %+v %v", figures, err)
	}
}

// Viewers reloading the Metrics page share the minute's probe: at most one
// exec into a project's database per probeEvery.
func TestThePostgresProbeRunsAtMostOncePerInterval(t *testing.T) {
	monitor := sampledMonitor(t)
	exec := &countingExec{fakeExec: fakeExec{output: "7|5|100|2048"}}
	databases := NewDatabases(monitor, exec)
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	databases.now = func() time.Time { return now }
	inst := project()[0]
	for range 5 {
		figures, err := databases.DatabaseFigures(context.Background(), inst)
		if err != nil || figures.Connections == nil || figures.Connections.Total != 7 || *figures.SizeBytes != 2048 {
			t.Fatalf("figures = %+v %v", figures, err)
		}
	}
	if exec.calls != 1 {
		t.Errorf("%d execs within one interval, want 1", exec.calls)
	}
	now = now.Add(probeEvery)
	if _, err := databases.DatabaseFigures(context.Background(), inst); err != nil || exec.calls != 2 {
		t.Errorf("after the interval: %d execs, %v", exec.calls, err)
	}
	exec.output, exec.err = "", errors.New("down")
	now = now.Add(probeEvery)
	figures, _ := databases.DatabaseFigures(context.Background(), inst)
	if figures.Connections != nil || exec.calls != 3 {
		t.Errorf("a failed probe is not answered from an older one: %+v", figures)
	}
	if _, _ = databases.DatabaseFigures(context.Background(), inst); exec.calls != 3 {
		t.Errorf("a failed probe is not retried within the interval: %d", exec.calls)
	}
}

type countingExec struct {
	fakeExec
	calls int
}

func (c *countingExec) ExecInContainerStdin(ctx context.Context, containerID string, cmd []string, stdin string) (string, error) {
	c.calls++
	return c.fakeExec.ExecInContainerStdin(ctx, containerID, cmd, stdin)
}

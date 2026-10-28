package hostmonitor

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeExec struct {
	output string
	err    error
	got    []string
	target string
}

func (f *fakeExec) ExecInContainerStdin(_ context.Context, containerID string, cmd []string, _ string) (string, error) {
	f.target, f.got = containerID, cmd
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
	if exec.target != "db-id" || !slices.Contains(exec.got, "shop") || exec.got[0] != "psql" || slices.Contains(exec.got, "sh") {
		t.Errorf("exec = %s %v; psql with the database as an argument, no shell", exec.target, exec.got)
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
	if !slices.Contains(exec.got, documentDBFiguresSQL) {
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

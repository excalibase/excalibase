package hostmonitor

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// Client connections by state, the server's cap and the data's size, as
// CNPG's exporter reports them on Kubernetes.
const (
	postgresFiguresSQL = `SELECT count(*) FILTER (WHERE backend_type = 'client backend'),
	count(*) FILTER (WHERE backend_type = 'client backend' AND state LIKE 'idle%'),
	current_setting('max_connections')::int, pg_database_size(current_database()) FROM pg_stat_activity`
	// A DocumentDB project's documents live in the postgres database (EXC-531).
	documentDBFiguresSQL = `SELECT count(*) FILTER (WHERE backend_type = 'client backend'),
	count(*) FILTER (WHERE backend_type = 'client backend' AND state LIKE 'idle%'),
	current_setting('max_connections')::int, pg_database_size(current_database()) + pg_database_size('postgres') FROM pg_stat_activity`
)

// Execer runs a command in a managed container, as the provisioners do.
type Execer interface {
	ExecInContainerStdin(ctx context.Context, containerID string, cmd []string, stdin string) (string, error)
}

// Databases reads database containers for the metrics loop.
type Databases struct {
	monitor *Monitor
	exec    Execer
}

func NewDatabases(monitor *Monitor, exec Execer) *Databases {
	return &Databases{monitor: monitor, exec: exec}
}

// DatabaseFigures joins the container's last sample with what Postgres says.
func (d *Databases) DatabaseFigures(ctx context.Context, inst *domain.DatabaseInstance) (service.SingleHostFigures, error) {
	var figures service.SingleHostFigures
	sample, sampled := d.monitor.Latest(inst.Namespace)
	if !sampled {
		container, err := d.monitor.deps.Engine.Inspect(ctx, inst.Namespace)
		if errors.Is(err, enginestats.ErrNotFound) {
			return figures, nil
		}
		if err != nil {
			return figures, err
		}
		sample = domain.ContainerSample{State: container.State}
	}
	figures.Running = sample.State == "running"
	if !figures.Running {
		return figures, nil
	}
	figures.CPUCores, figures.MemoryBytes = sample.CPUCores, sample.MemoryBytes
	if inst.DBType == domain.PostgreSQL && d.exec != nil {
		d.addPostgres(ctx, inst, &figures)
	}
	return figures, nil
}

func (d *Databases) addPostgres(ctx context.Context, inst *domain.DatabaseInstance, figures *service.SingleHostFigures) {
	query := postgresFiguresSQL
	if inst.DocumentDB {
		query = documentDBFiguresSQL
	}
	database := inst.DatabaseName
	if database == "" {
		database = "postgres"
	}
	out, err := d.exec.ExecInContainerStdin(ctx, inst.Namespace,
		[]string{"psql", "-U", "postgres", "-d", database, "-XAt", "-F", "|", "-c", query}, "")
	if err != nil {
		log.Printf("metrics of %s: postgres figures: %v", inst.ProjectID, err)
		return
	}
	values, ok := parseFigures(out)
	if !ok {
		log.Printf("metrics of %s: postgres figures: unexpected answer", inst.ProjectID)
		return
	}
	figures.Connections = &service.DatabaseConnections{Total: int(values[0]), Idle: int(values[1]), Max: int(values[2])}
	figures.SizeBytes = &values[3]
}

// parseFigures reads the last line psql printed: four integers.
func parseFigures(out string) ([4]int64, bool) {
	var values [4]int64
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Split(strings.TrimSpace(lines[len(lines)-1]), "|")
	if len(fields) != len(values) {
		return values, false
	}
	for i, field := range fields {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return values, false
		}
		values[i] = value
	}
	return values, true
}

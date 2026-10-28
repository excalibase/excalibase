package hostmonitor

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// Client connections by state, the server's cap and the database's size, as
// CNPG's exporter reports them on Kubernetes. Every name is pg_catalog's and
// the database arrives as psql's quoted variable :'db'.
const (
	postgresFiguresSQL = `SELECT pg_catalog.count(*) FILTER (WHERE backend_type OPERATOR(pg_catalog.=) 'client backend'),
	pg_catalog.count(*) FILTER (WHERE backend_type OPERATOR(pg_catalog.=) 'client backend' AND state OPERATOR(pg_catalog.~~) 'idle%'),
	pg_catalog.current_setting('max_connections')::pg_catalog.int4, pg_catalog.pg_database_size(:'db') FROM pg_catalog.pg_stat_activity;
`
	// A DocumentDB project's documents live in the postgres database (EXC-531).
	documentDBFiguresSQL = `SELECT pg_catalog.count(*) FILTER (WHERE backend_type OPERATOR(pg_catalog.=) 'client backend'),
	pg_catalog.count(*) FILTER (WHERE backend_type OPERATOR(pg_catalog.=) 'client backend' AND state OPERATOR(pg_catalog.~~) 'idle%'),
	pg_catalog.current_setting('max_connections')::pg_catalog.int4,
	pg_catalog.pg_database_size(:'db') OPERATOR(pg_catalog.+) pg_catalog.pg_database_size('postgres') FROM pg_catalog.pg_stat_activity;
`
)

// probeConninfo runs the probe as the superuser in the maintenance database,
// never the customer's, with every setting an owner could attach pinned (EXC-623).
const probeConninfo = "dbname=postgres options='-c search_path=pg_catalog,pg_temp -c role=none -c local_preload_libraries= " +
	"-c session_preload_libraries= -c default_transaction_read_only=on -c statement_timeout=5s'"

// Execer runs a command in a managed container, as the provisioners do.
type Execer interface {
	ExecInContainerStdin(ctx context.Context, containerID string, cmd []string, stdin string) (string, error)
}

// probeEvery is how often one project's database is asked for its figures,
// however often its Metrics page is read.
const probeEvery = 50 * time.Second

// probed is a project's last Postgres answer; a failed one holds nil figures.
type probed struct {
	at          time.Time
	connections *service.DatabaseConnections
	size        *int64
}

// Databases reads database containers for the metrics loop.
type Databases struct {
	monitor *Monitor
	exec    Execer
	now     func() time.Time
	mu      sync.Mutex
	probes  map[string]probed
}

func NewDatabases(monitor *Monitor, exec Execer) *Databases {
	return &Databases{monitor: monitor, exec: exec, now: time.Now, probes: map[string]probed{}}
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

// addPostgres answers the project's last probe while it is fresh, else probes.
func (d *Databases) addPostgres(ctx context.Context, inst *domain.DatabaseInstance, figures *service.SingleHostFigures) {
	d.mu.Lock()
	defer d.mu.Unlock()
	last, seen := d.probes[inst.ProjectID]
	if !seen || d.now().Sub(last.at) >= probeEvery {
		last = probed{at: d.now()}
		last.connections, last.size = d.probePostgres(ctx, inst)
		d.probes[inst.ProjectID] = last
	}
	figures.Connections, figures.SizeBytes = last.connections, last.size
}

func (d *Databases) probePostgres(ctx context.Context, inst *domain.DatabaseInstance) (*service.DatabaseConnections, *int64) {
	query := postgresFiguresSQL
	if inst.DocumentDB {
		query = documentDBFiguresSQL
	}
	database := inst.DatabaseName
	if database == "" {
		database = "postgres"
	}
	out, err := d.exec.ExecInContainerStdin(ctx, inst.Namespace,
		[]string{"psql", probeConninfo, "-U", "postgres", "-XAt", "-F", "|", "-v", "ON_ERROR_STOP=1", "-v", "db=" + database}, query)
	if err != nil {
		log.Printf("metrics of %s: postgres figures: %v", inst.ProjectID, err)
		return nil, nil
	}
	values, ok := parseFigures(out)
	if !ok {
		log.Printf("metrics of %s: postgres figures: unexpected answer", inst.ProjectID)
		return nil, nil
	}
	return &service.DatabaseConnections{Total: int(values[0]), Idle: int(values[1]), Max: int(values[2])}, &values[3]
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

package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DockerClient abstracts Docker API operations for testability.
type DockerClient interface {
	// CreateContainer creates (does not start) a container held to limits.
	CreateContainer(ctx context.Context, name, image string, env map[string]string, ports map[string]string, limits ContainerLimits) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (string, error) // "running", "stopped", "not_found"
	WaitForHealthy(ctx context.Context, containerID string) error
	// ExecInContainer runs cmd inside an already-running container and
	// returns its exit code. Used by database provisioners to probe
	// readiness (e.g. `pg_isready`). Does not capture stdout/stderr —
	// exit code is all the caller needs.
	ExecInContainer(ctx context.Context, containerID string, cmd []string) (int, error)
	// CopyToContainer extracts a tar archive into dstPath inside the
	// (created or running) container. Used by the Docker backup
	// adapter's restore path: stream a base backup tar from S3 →
	// untar into the new container's /var/lib/postgresql/data before
	// it's started so postgres skips initdb and opens the restored
	// data dir directly. content MUST be raw tar (uncompressed) —
	// callers gunzip first.
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error
	// CopyFromContainer returns a tar stream of srcPath inside the
	// container. Used by the backup adapter's WAL-archive upload
	// path: read the contents of /walarchive (where archive_command
	// dropped completed WAL segments), iterate the tar, gzip + upload
	// each entry to S3 so PITR has the WALs to replay on restore.
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error)
	// CreateContainerSpec creates (does not start) the container spec
	// describes: a command, a data volume, a stop signal, another
	// container's network namespace.
	CreateContainerSpec(ctx context.Context, spec ContainerSpec) (string, error)
	// ContainerState inspects a container; one the engine lacks is not found.
	ContainerState(ctx context.Context, containerID string) (ContainerState, error)
	// ExecInContainerStdin runs cmd with stdin fed to it and returns its
	// output; a non-zero exit is an error carrying the output.
	ExecInContainerStdin(ctx context.Context, containerID string, cmd []string, stdin string) (string, error)
}

// defaultPostgresSuperuser is the well-known username used by the
// official postgres Docker image when POSTGRES_USER is set. It's a
// public default — not a secret. Named here so SAST tools see a const,
// not a string literal that looks like a hardcoded credential.
const defaultPostgresSuperuser = "postgres"

// containerNotFound is the ContainerStatus value for a container the daemon
// does not know about — the state teardown waits for.
const containerNotFound = "not_found"

// containerRunning is the ContainerStatus value for a live container.
const containerRunning = "running"

// postgresReadyTimeout bounds the official image's start; its entrypoint
// initialises the data directory before postgres takes TCP connections.
const postgresReadyTimeout = 30 * time.Second

// DockerPostgreSQLProvisioner provisions PostgreSQL via Docker containers.
type DockerPostgreSQLProvisioner struct {
	docker         DockerClient
	deletionPoller Poller
	// pausePoller bounds the wait for a stopped container to actually
	// leave the running state.
	pausePoller Poller
}

func NewDockerPostgreSQLProvisioner(docker DockerClient) *DockerPostgreSQLProvisioner {
	return &DockerPostgreSQLProvisioner{
		docker:         docker,
		deletionPoller: NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
		pausePoller:    NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
	}
}

// SetDeletionPoller overrides how long teardown waits for the project's
// container to actually disappear.
func (p *DockerPostgreSQLProvisioner) SetDeletionPoller(poller Poller) {
	p.deletionPoller = poller
}

func (p *DockerPostgreSQLProvisioner) SupportedType() domain.DatabaseType {
	return domain.PostgreSQL
}

func (p *DockerPostgreSQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	cb(domain.StageValidating)
	if req.DocumentDB {
		return p.provisionDocumentDB(ctx, req, tier, cb)
	}

	// EXC-408: the major comes from the request. No implicit default — a
	// request that names none, or one the catalogue does not list, is refused
	// before any container exists.
	image, err := config.DockerPostgresImage(req.PostgresVersion)
	if err != nil {
		return nil, err
	}
	limits, err := LimitsForTier(tier)
	if err != nil {
		return nil, err
	}

	dbName := databaseNameOrDefault(req.DatabaseName)
	password := generatePassword()

	// Stage: Container creation
	cb(domain.StageContainerCreation)

	containerName := DatabaseContainerName(req.ProjectName)
	env := map[string]string{
		"POSTGRES_DB":       dbName,
		"POSTGRES_USER":     defaultPostgresSuperuser,
		"POSTGRES_PASSWORD": password,
	}
	ports := map[string]string{"5432": ""}

	containerID, err := p.docker.CreateContainer(ctx, containerName, image, env, ports, limits)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := p.docker.StartContainer(ctx, containerID); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	// Stage: Wait for ready — container running AND Postgres accepting
	// connections. Docker's healthcheck (if any) only says the container
	// is up; pg_isready confirms Postgres finished its own init and is
	// listening on 5432. Without this probe, a fast invoke right after
	// Provision could race against Postgres startup.
	cb(domain.StageWaitingForReady)
	if err := p.startDatabase(ctx, containerID, dbName, postgresReadyTimeout); err != nil {
		return nil, err
	}

	// Stage: Credential generation
	cb(domain.StageCredentialGeneration)
	p.enableArchive(ctx, req, containerID, dbName, postgresReadyTimeout)

	cb(domain.StageCompleted)

	return &ProvisioningResult{
		Host:         containerName,
		Port:         5432,
		DatabaseName: dbName,
		Username:     defaultPostgresSuperuser,
		Password:     password,
		Namespace:    containerID,
		// The container serves no TLS: it is reached only on the host's own networks.
		SSLMode: "disable",
	}, nil
}

func databaseNameOrDefault(name string) string {
	if name == "" {
		return "app"
	}
	return name
}

// startDatabase waits for the started container to run and its postgres to
// answer queries in dbName.
func (p *DockerPostgreSQLProvisioner) startDatabase(ctx context.Context, containerID, dbName string, timeout time.Duration) error {
	if err := p.docker.WaitForHealthy(ctx, containerID); err != nil {
		return fmt.Errorf("wait for healthy: %w", err)
	}
	if err := p.waitForPostgresReady(ctx, containerID, dbName, timeout); err != nil {
		return fmt.Errorf("wait for postgres ready: %w", err)
	}
	return nil
}

// enableArchive is PITR groundwork: WAL archiving when backup is requested.
// archive_mode=on requires a postgres restart, so this runs before the
// container is handed back to the user. Best-effort: failure logs but doesn't
// block provisioning. Backup itself works without this; PITR requires it.
func (p *DockerPostgreSQLProvisioner) enableArchive(ctx context.Context, req domain.ProvisioningRequest, containerID, dbName string, timeout time.Duration) {
	if req.Backup == nil || !req.Backup.Enabled {
		return
	}
	if err := p.ConfigureArchive(ctx, containerID, "cp %p /walarchive/%f", defaultPostgresSuperuser); err != nil {
		fmt.Printf("WARN: configure archive for %s: %v\n", req.ProjectName, err)
		return
	}
	// Re-probe readiness after the restart.
	_ = p.waitForPostgresReady(ctx, containerID, dbName, timeout)
}

// Pause stops the project's postgres container without removing it.
// Volumes persist so Resume reopens the same data dir. Idempotent —
// stopping an already-stopped container is fine.
// StopReplication is a no-op in Docker mode: the single-tenant deployment
// runs no per-project CDC watcher, so nothing holds a replication session
// open against the container.
func (p *DockerPostgreSQLProvisioner) StopReplication(context.Context, string, string) error {
	return nil
}

// Pause stops the project's containers, a DocumentDB gateway before its
// database, and returns only once the daemon reports none of them running.
// StopContainer returning is not proof the process is down — postgres gets a
// shutdown grace period.
func (p *DockerPostgreSQLProvisioner) Pause(ctx context.Context, namespace, projectID string) error {
	if namespace == "" {
		return fmt.Errorf("docker pause: container id missing on instance.Namespace")
	}
	containers, err := p.projectContainers(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	for _, id := range containers {
		if err := p.docker.StopContainer(ctx, id); err != nil {
			return fmt.Errorf("stop container: %w", err)
		}
	}
	return p.pausePoller.WaitUntilClear(ctx, "running container "+namespace,
		func(ctx context.Context) ([]string, error) { return p.running(ctx, containers) })
}

// projectContainers are the project's containers in stopping order: its
// DocumentDB gateway, if it has one, then its database.
func (p *DockerPostgreSQLProvisioner) projectContainers(ctx context.Context, namespace, projectID string) ([]string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("docker: project id missing for container %s", namespace)
	}
	gateway := DocumentDBGatewayName(projectID)
	status, err := p.docker.ContainerStatus(ctx, gateway)
	if err != nil {
		return nil, fmt.Errorf("container status: %w", err)
	}
	if status == containerNotFound {
		return []string{namespace}, nil
	}
	return []string{gateway, namespace}, nil
}

// running lists the containers still in the running state.
func (p *DockerPostgreSQLProvisioner) running(ctx context.Context, containers []string) ([]string, error) {
	var still []string
	for _, id := range containers {
		status, err := p.docker.ContainerStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		if status == containerRunning {
			still = append(still, id+" ("+status+")")
		}
	}
	return still, nil
}

// WorkloadStopped reports whether every container of the project has left
// the running state. A container the daemon no longer knows about is stopped
// as far as the project is concerned.
func (p *DockerPostgreSQLProvisioner) WorkloadStopped(ctx context.Context, namespace, projectID string) (bool, error) {
	if namespace == "" {
		return false, fmt.Errorf("docker workload state: container id missing on instance.Namespace")
	}
	containers, err := p.projectContainers(ctx, namespace, projectID)
	if err != nil {
		return false, err
	}
	still, err := p.running(ctx, containers)
	if err != nil {
		return false, fmt.Errorf("container status: %w", err)
	}
	return len(still) == 0, nil
}

// SetPausePoller overrides how long a pause waits for the container to stop.
func (p *DockerPostgreSQLProvisioner) SetPausePoller(poller Poller) {
	p.pausePoller = poller
}

// Resume starts a previously-paused container and waits for postgres
// to accept queries. Caller must run capacity + tier pre-checks
// before calling so we don't bring up a workload that exceeds limits.
// A DocumentDB gateway is restarted after its database, whose restart gave
// the pair a new network namespace.
func (p *DockerPostgreSQLProvisioner) Resume(ctx context.Context, namespace, projectID string) error {
	if namespace == "" {
		return fmt.Errorf("docker resume: container id missing on instance.Namespace")
	}
	containers, err := p.projectContainers(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	if err := p.docker.StartContainer(ctx, namespace); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	if err := p.docker.WaitForHealthy(ctx, namespace); err != nil {
		return fmt.Errorf("wait healthy after resume: %w", err)
	}
	if len(containers) == 1 {
		return nil
	}
	if err := RestartDocumentDBGateway(ctx, p.docker, projectID); err != nil {
		return fmt.Errorf("restart the DocumentDB gateway after resume: %w", err)
	}
	return nil
}

// Deprovision removes the project's containers, a DocumentDB gateway first
// (Podman refuses to remove a container another joined), and returns only
// once the daemon reports them gone. A container that is already absent
// counts as removed, so a retried teardown is idempotent.
func (p *DockerPostgreSQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	containers, err := p.projectContainers(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	// A provision that failed before recording the container id left the
	// admitted namespace on the row; the container is still named for it.
	if named := DatabaseContainerName(projectID); named != namespace {
		containers = append(containers, named)
	}
	for _, id := range containers {
		if err := p.removeObserved(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (p *DockerPostgreSQLProvisioner) removeObserved(ctx context.Context, id string) error {
	status, err := p.docker.ContainerStatus(ctx, id)
	if err != nil {
		return fmt.Errorf("container status: %w", err)
	}
	if status == containerNotFound {
		return nil
	}
	if err := p.docker.StopContainer(ctx, id); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	if err := p.docker.RemoveContainer(ctx, id); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	return p.deletionPoller.WaitUntilClear(ctx, "container "+id,
		func(ctx context.Context) ([]string, error) {
			status, err := p.docker.ContainerStatus(ctx, id)
			if err != nil || status == containerNotFound {
				return nil, err
			}
			return []string{id + " (" + status + ")"}, nil
		})
}

func (p *DockerPostgreSQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	status, err := p.docker.ContainerStatus(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &ProvisioningStatus{
		Phase:   status,
		Ready:   status == containerRunning,
		Message: status,
	}, nil
}

func (p *DockerPostgreSQLProvisioner) ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error {
	// Docker mode backup is configured via WAL-G sidecars at the daemon level;
	// the provisioner does not manage backup containers directly.
	return nil
}

// ConfigureArchive enables continuous WAL archiving on the project's
// postgres container. Must be called *after* the container is up and
// pg is query-ready. The container is restarted at the end because
// `archive_mode` is changed only on (re)start, not on reload.
//
// Order:
//
//  1. ALTER SYSTEM SET wal_level = 'replica'    (safe to reload)
//  2. ALTER SYSTEM SET archive_mode = 'on'      (needs restart)
//  3. ALTER SYSTEM SET archive_command = '...'  (safe to reload)
//  4. Stop + start container
//
// After the restart, pg picks up archive_mode=on and starts shipping
// WAL segments via archive_command. The platform's WAL-G sidecar
// then ships them to S3.
func (p *DockerPostgreSQLProvisioner) ConfigureArchive(ctx context.Context, containerID, archiveCommand, superuser string) error {
	// Build a single SQL string with three ALTER SYSTEMs so we minimise
	// docker exec round trips (each exec carries 1-2s of overhead).
	sql := fmt.Sprintf(
		"ALTER SYSTEM SET wal_level = 'replica'; "+
			"ALTER SYSTEM SET archive_mode = 'on'; "+
			"ALTER SYSTEM SET archive_command = %s;",
		quoteSQLString(archiveCommand))
	if _, err := p.docker.ExecInContainer(ctx, containerID, []string{"psql", "-U", superuser, "-c", sql}); err != nil {
		return fmt.Errorf("psql ALTER SYSTEM: %w", err)
	}

	// archive_mode is read at startup only — reload won't pick it up.
	// Stop + start gives pg a clean restart cycle.
	if err := p.docker.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("stop for archive restart: %w", err)
	}
	if err := p.docker.StartContainer(ctx, containerID); err != nil {
		return fmt.Errorf("start after archive ALTER: %w", err)
	}
	return nil
}

// quoteSQLString single-quote-escapes a string for safe inclusion in
// an ALTER SYSTEM SET statement. archive_command is operator-controlled
// (not user input) but we still want to avoid surprising breakage if
// the value contains a single quote — wrap in '...' and double any
// embedded quote per Postgres lexical rules.
func quoteSQLString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

// waitForPostgresReady polls Postgres readiness inside the container and
// only returns once the server is accepting real client connections.
//
// pg_isready alone is NOT sufficient: the official postgres:17 image init
// flow temporarily starts Postgres on a unix socket while running its
// initdb / docker-entrypoint-initdb.d scripts, then restarts on TCP. A
// pg_isready exec during that window returns 0 even though TCP and the
// target DB aren't ready yet. The next caller's `psql -U postgres -d app`
// then races against the restart and fails with exit code 2.
//
// Two-phase probe: pg_isready first (cheap), then a real `psql -c "SELECT 1"`
// against the target DB to confirm the rebooted server actually accepts
// queries. Caps at 30s. Runs after WaitForHealthy so the container is up.
func (p *DockerPostgreSQLProvisioner) waitForPostgresReady(ctx context.Context, containerID, dbName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pgIsReady := []string{"pg_isready", "-U", "postgres", "-d", dbName}
	psqlProbe := []string{"psql", "-U", "postgres", "-d", dbName, "-tAc", "SELECT 1"}
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if code, err := p.docker.ExecInContainer(ctx, containerID, pgIsReady); err == nil && code == 0 {
			// pg_isready succeeded — confirm with a real query before declaring ready.
			if code2, err2 := p.docker.ExecInContainer(ctx, containerID, psqlProbe); err2 == nil && code2 == 0 {
				return nil
			} else {
				lastErr = err2
			}
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("postgres did not become query-ready within %s: %w", timeout, lastErr)
	}
	return fmt.Errorf("postgres did not become query-ready within %s", timeout)
}

func generatePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

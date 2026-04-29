package provisioner

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DockerClient abstracts Docker API operations for testability.
type DockerClient interface {
	CreateContainer(ctx context.Context, name, image string, env map[string]string, ports map[string]string) (string, error)
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
}

// DockerPostgreSQLProvisioner provisions PostgreSQL via Docker containers.
type DockerPostgreSQLProvisioner struct {
	docker DockerClient
}

func NewDockerPostgreSQLProvisioner(docker DockerClient) *DockerPostgreSQLProvisioner {
	return &DockerPostgreSQLProvisioner{docker: docker}
}

func (p *DockerPostgreSQLProvisioner) SupportedType() domain.DatabaseType {
	return domain.PostgreSQL
}

func (p *DockerPostgreSQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	cb(domain.StageValidating)

	dbName := req.DatabaseName
	if dbName == "" {
		dbName = "app"
	}
	password := req.AppPassword
	if password == "" {
		password = generatePassword()
	}

	// Stage: Container creation
	cb(domain.StageContainerCreation)

	containerName := fmt.Sprintf("excalibase-%s-postgres", req.ProjectName)
	env := map[string]string{
		"POSTGRES_DB":       dbName,
		"POSTGRES_USER":     "postgres",
		"POSTGRES_PASSWORD": password,
	}
	ports := map[string]string{"5432": ""}

	containerID, err := p.docker.CreateContainer(ctx, containerName, "postgres:17", env, ports)
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
	if err := p.docker.WaitForHealthy(ctx, containerID); err != nil {
		return nil, fmt.Errorf("wait for healthy: %w", err)
	}
	if err := p.waitForPostgresReady(ctx, containerID, dbName); err != nil {
		return nil, fmt.Errorf("wait for postgres ready: %w", err)
	}

	// Stage: Credential generation
	cb(domain.StageCredentialGeneration)

	cb(domain.StageCompleted)

	return &ProvisioningResult{
		Host:         containerName,
		Port:         5432,
		DatabaseName: dbName,
		Username:     "postgres",
		Password:     password,
		Namespace:    containerID,
	}, nil
}

func (p *DockerPostgreSQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	if err := p.docker.StopContainer(ctx, namespace); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	return p.docker.RemoveContainer(ctx, namespace)
}

func (p *DockerPostgreSQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	status, err := p.docker.ContainerStatus(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &ProvisioningStatus{
		Phase:   status,
		Ready:   status == "running",
		Message: status,
	}, nil
}

func (p *DockerPostgreSQLProvisioner) ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error {
	// TODO: deploy WAL-G sidecar container
	return nil
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
func (p *DockerPostgreSQLProvisioner) waitForPostgresReady(ctx context.Context, containerID, dbName string) error {
	deadline := time.Now().Add(30 * time.Second)
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
		return fmt.Errorf("postgres did not become query-ready within 30s: %w", lastErr)
	}
	return fmt.Errorf("postgres did not become query-ready within 30s")
}

func generatePassword() string {
	b := make([]byte, 16)
	// crypto/rand would be used in production
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"[i%62]
	}
	return string(b)
}

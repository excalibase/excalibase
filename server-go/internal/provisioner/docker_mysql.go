package provisioner

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DockerMySQLProvisioner runs MySQL projects as one container each on a
// single host (EXC-592): one instance, HA stays on Kubernetes (ADR 0038).
// Every lifecycle step waits on a real query over the socket, never on the
// container's state alone.
type DockerMySQLProvisioner struct {
	docker DockerClient
	// readyPoller bounds a wait for mysqld to answer; stopPoller a wait for
	// the container to stop or go.
	readyPoller Poller
	stopPoller  Poller
}

// defaultDockerMySQLReady covers an image pull and datadir initialisation on
// a slow disk.
const defaultDockerMySQLReady = 3 * time.Minute

func NewDockerMySQLProvisioner(docker DockerClient) *DockerMySQLProvisioner {
	return &DockerMySQLProvisioner{
		docker:      docker,
		readyPoller: NewPoller(time.Second, defaultDockerMySQLReady),
		stopPoller:  NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
	}
}

// SetPollers overrides how long the provisioner waits for mysqld to answer
// and for the container to stop or go.
func (p *DockerMySQLProvisioner) SetPollers(ready, stop Poller) {
	p.readyPoller, p.stopPoller = ready, stop
}

func (p *DockerMySQLProvisioner) SupportedType() domain.DatabaseType { return domain.MySQL }

// Provision creates and starts the container, waits for a query and creates
// the project's database. The platform users are created at registration.
func (p *DockerMySQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	cb(domain.StageValidating)
	// A single host's backups are the platform's, not MOCO's to validate.
	shape := req
	shape.Backup = nil
	database, image, err := validateMySQLRequest(shape)
	if err != nil {
		return nil, err
	}
	spec, err := MySQLContainerSpec(req.ProjectName, image, tier)
	if err != nil {
		return nil, err
	}
	cb(domain.StageContainerCreation)
	if _, err := p.docker.CreateContainerSpec(ctx, spec); err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	if err := p.docker.StartContainer(ctx, spec.Name); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}
	cb(domain.StageWaitingForReady)
	if err := p.AwaitServing(ctx, spec.Name); err != nil {
		return nil, err
	}
	cb(domain.StageCredentialGeneration)
	if _, err := p.run(ctx, spec.Name, "CREATE DATABASE IF NOT EXISTS "+mysqlIdent(database)+";"); err != nil {
		return nil, fmt.Errorf("create database %s: %w", database, err)
	}
	cb(domain.StageCompleted)
	return &ProvisioningResult{
		Host:         spec.Name,
		Port:         mysqlPort,
		DatabaseName: database,
		Username:     MySQLUserAdmin,
		SSLMode:      "REQUIRED",
		Namespace:    spec.Name,
	}, nil
}

// AwaitServing returns once a query answers over the socket.
func (p *DockerMySQLProvisioner) AwaitServing(ctx context.Context, container string) error {
	if err := p.readyPoller.WaitUntilReady(ctx, "MySQL in "+container, func(ctx context.Context) (bool, error) {
		return p.serving(ctx, container), nil
	}); err != nil {
		return fmt.Errorf("wait for mysql to answer: %w", err)
	}
	return nil
}

// serving reports whether the container runs and mysqld answers a query.
func (p *DockerMySQLProvisioner) serving(ctx context.Context, container string) bool {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil || status != containerRunning {
		return false
	}
	code, err := p.docker.ExecInContainer(ctx, container, append(mysqlRootClient(), "-e", "SELECT 1"))
	return err == nil && code == 0
}

// GetStatus is the observed state: the container and a real query.
func (p *DockerMySQLProvisioner) GetStatus(ctx context.Context, namespace, _ string) (*ProvisioningStatus, error) {
	if p.serving(ctx, namespace) {
		return &ProvisioningStatus{Phase: "Running", Ready: true, Message: containerRunning}, nil
	}
	status, err := p.docker.ContainerStatus(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &ProvisioningStatus{Phase: "Pending", Message: status}, nil
}

// ConfigureBackup has nothing to do: the platform takes a single host's
// MySQL backups on the plan's interval.
func (p *DockerMySQLProvisioner) ConfigureBackup(context.Context, string, string, string, int) error {
	return nil
}

// StopReplication has nothing to stop: a single host runs no CDC watcher.
func (p *DockerMySQLProvisioner) StopReplication(context.Context, string, string) error { return nil }

// Pause stops the container, keeping its volume, and returns once it is
// observed stopped.
func (p *DockerMySQLProvisioner) Pause(ctx context.Context, namespace, _ string) error {
	if namespace == "" {
		return fmt.Errorf("docker pause: the project's container is not recorded")
	}
	if err := p.docker.StopContainer(ctx, namespace); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	return p.stopPoller.WaitUntilClear(ctx, "running container "+namespace, func(ctx context.Context) ([]string, error) {
		return p.stillRunning(ctx, namespace)
	})
}

func (p *DockerMySQLProvisioner) stillRunning(ctx context.Context, container string) ([]string, error) {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil || status != containerRunning {
		return nil, err
	}
	return []string{container}, nil
}

// Resume starts the container and returns once mysqld answers.
func (p *DockerMySQLProvisioner) Resume(ctx context.Context, namespace, _ string) error {
	if namespace == "" {
		return fmt.Errorf("docker resume: the project's container is not recorded")
	}
	if err := p.docker.StartContainer(ctx, namespace); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return p.AwaitServing(ctx, namespace)
}

// WorkloadStopped reports whether the container is not running; one the
// engine no longer knows is stopped as far as the project is concerned.
func (p *DockerMySQLProvisioner) WorkloadStopped(ctx context.Context, namespace, _ string) (bool, error) {
	running, err := p.stillRunning(ctx, namespace)
	if err != nil {
		return false, fmt.Errorf("container status: %w", err)
	}
	return len(running) == 0, nil
}

// Deprovision removes the container with its volume and returns once it is
// observed gone. An absent container counts as removed.
func (p *DockerMySQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	container := namespace
	if container == "" {
		container = MySQLContainerName(projectID)
	}
	return p.RemoveContainer(ctx, container)
}

// RemoveContainer stops and removes a container and waits until it is gone.
func (p *DockerMySQLProvisioner) RemoveContainer(ctx context.Context, container string) error {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil {
		return fmt.Errorf("container status: %w", err)
	}
	if status == containerNotFound {
		return nil
	}
	if err := p.docker.StopContainer(ctx, container); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	if err := p.docker.RemoveContainer(ctx, container); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	return p.stopPoller.WaitUntilClear(ctx, "container "+container, func(ctx context.Context) ([]string, error) {
		status, err := p.docker.ContainerStatus(ctx, container)
		if err != nil || status == containerNotFound {
			return nil, err
		}
		return []string{container + " (" + status + ")"}, nil
	})
}

package provisioner

import (
	"context"
	"fmt"

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

	// Stage: Wait for ready
	cb(domain.StageWaitingForReady)
	if err := p.docker.WaitForHealthy(ctx, containerID); err != nil {
		return nil, fmt.Errorf("wait for healthy: %w", err)
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

func generatePassword() string {
	b := make([]byte, 16)
	// crypto/rand would be used in production
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"[i%62]
	}
	return string(b)
}

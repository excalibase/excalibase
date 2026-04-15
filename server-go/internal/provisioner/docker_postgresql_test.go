package provisioner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	containers map[string]string // containerID → status
	failOn     string
}

func newMockDocker() *mockDockerClient {
	return &mockDockerClient{containers: make(map[string]string)}
}

func (m *mockDockerClient) CreateContainer(_ context.Context, name, image string, env map[string]string, ports map[string]string) (string, error) {
	if m.failOn == "create" {
		return "", fmt.Errorf("create failed")
	}
	id := "container-" + name
	m.containers[id] = "created"
	return id, nil
}

func (m *mockDockerClient) StartContainer(_ context.Context, containerID string) error {
	if m.failOn == "start" {
		return fmt.Errorf("start failed")
	}
	m.containers[containerID] = "running"
	return nil
}

func (m *mockDockerClient) StopContainer(_ context.Context, containerID string) error {
	if m.failOn == "stop" {
		return fmt.Errorf("stop failed")
	}
	m.containers[containerID] = "stopped"
	return nil
}

func (m *mockDockerClient) RemoveContainer(_ context.Context, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

func (m *mockDockerClient) ContainerStatus(_ context.Context, containerID string) (string, error) {
	status, ok := m.containers[containerID]
	if !ok {
		return "not_found", nil
	}
	return status, nil
}

func (m *mockDockerClient) WaitForHealthy(_ context.Context, containerID string) error {
	if m.failOn == "health" {
		return fmt.Errorf("health check failed")
	}
	return nil
}

func (m *mockDockerClient) ExecInContainer(_ context.Context, containerID string, cmd []string) (int, error) {
	if m.failOn == "exec" {
		return 2, nil // pg_isready exit 2 = no connection attempt
	}
	return 0, nil
}

func TestDockerProvisioner_SupportedType(t *testing.T) {
	p := NewDockerPostgreSQLProvisioner(newMockDocker())
	if p.SupportedType() != domain.PostgreSQL {
		t.Errorf("expected PostgreSQL, got %s", p.SupportedType())
	}
}

func TestDockerProvisioner_Provision(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)

	var stages []domain.ProvisioningStage
	cb := func(s domain.ProvisioningStage) { stages = append(stages, s) }

	req := domain.ProvisioningRequest{ProjectName: "my-app", DBType: domain.PostgreSQL}
	tier := config.TierConfig{}

	result, err := p.Provision(context.Background(), req, tier, cb)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if result.Port != 5432 {
		t.Errorf("expected port 5432, got %d", result.Port)
	}
	if result.DatabaseName != "app" {
		t.Errorf("expected db=app, got %s", result.DatabaseName)
	}
	if result.Username != "postgres" {
		t.Errorf("expected user=postgres, got %s", result.Username)
	}
	if result.Password == "" {
		t.Error("expected non-empty password")
	}
	if result.Host == "" {
		t.Error("expected non-empty host")
	}

	// Verify stages
	expected := []domain.ProvisioningStage{
		domain.StageValidating,
		domain.StageContainerCreation,
		domain.StageWaitingForReady,
		domain.StageCredentialGeneration,
		domain.StageCompleted,
	}
	if len(stages) != len(expected) {
		t.Fatalf("expected %d stages, got %d: %v", len(expected), len(stages), stages)
	}
	for i, s := range expected {
		if stages[i] != s {
			t.Errorf("stage[%d]: expected %s, got %s", i, s, stages[i])
		}
	}

	// Container should be running
	status, _ := docker.ContainerStatus(context.Background(), result.Namespace)
	if status != "running" {
		t.Errorf("expected running, got %s", status)
	}
}

func TestDockerProvisioner_Deprovision(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)

	// Provision first
	req := domain.ProvisioningRequest{ProjectName: "to-delete", DBType: domain.PostgreSQL}
	result, _ := p.Provision(context.Background(), req, config.TierConfig{}, func(s domain.ProvisioningStage) {})

	// Deprovision
	err := p.Deprovision(context.Background(), result.Namespace, "to-delete")
	if err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	// Container should be gone
	status, _ := docker.ContainerStatus(context.Background(), result.Namespace)
	if status != "not_found" {
		t.Errorf("expected not_found, got %s", status)
	}
}

func TestDockerProvisioner_GetStatus(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)

	req := domain.ProvisioningRequest{ProjectName: "status-test", DBType: domain.PostgreSQL}
	result, _ := p.Provision(context.Background(), req, config.TierConfig{}, func(s domain.ProvisioningStage) {})

	status, err := p.GetStatus(context.Background(), result.Namespace, "status-test")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !status.Ready {
		t.Error("expected ready=true")
	}
	if status.Phase != "running" {
		t.Errorf("expected phase=running, got %s", status.Phase)
	}
}

func TestDockerProvisioner_CreateFails(t *testing.T) {
	docker := newMockDocker()
	docker.failOn = "create"
	p := NewDockerPostgreSQLProvisioner(docker)

	_, err := p.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "fail"}, config.TierConfig{}, func(s domain.ProvisioningStage) {})
	if err == nil {
		t.Error("expected error when create fails")
	}
}

func TestDockerProvisioner_HealthCheckFails(t *testing.T) {
	docker := newMockDocker()
	docker.failOn = "health"
	p := NewDockerPostgreSQLProvisioner(docker)

	_, err := p.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "unhealthy"}, config.TierConfig{}, func(s domain.ProvisioningStage) {})
	if err == nil {
		t.Error("expected error when health check fails")
	}
}

// TestDockerProvisioner_PgReadyNeverSucceeds confirms the pg_isready probe
// errors out when the DB never reports ready. The mock returns exit=2 for
// every probe when failOn="exec", so the 30s loop should give up.
func TestDockerProvisioner_PgReadyNeverSucceeds(t *testing.T) {
	docker := newMockDocker()
	docker.failOn = "exec"
	p := NewDockerPostgreSQLProvisioner(docker)

	// Short-deadline context so we don't wait a real 30s for the loop.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := p.Provision(ctx, domain.ProvisioningRequest{ProjectName: "stuck"}, config.TierConfig{}, func(s domain.ProvisioningStage) {})
	if err == nil {
		t.Error("expected error when pg_isready never returns 0")
	}
}

func TestDockerProvisioner_FactoryRegistration(t *testing.T) {
	docker := newMockDocker()
	dp := NewDockerPostgreSQLProvisioner(docker)
	factory := NewFactory(dp)

	p, ok := factory.Get(domain.PostgreSQL)
	if !ok {
		t.Fatal("expected Docker provisioner registered")
	}
	if p != dp {
		t.Error("expected same provisioner instance")
	}
}

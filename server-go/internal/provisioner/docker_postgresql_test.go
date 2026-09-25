package provisioner

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	containers map[string]string // containerID → status
	failOn     string
	execLog    [][]string // every Exec invocation's cmd captured here
	stops      int        // counter for StopContainer calls
	starts     int        // counter for StartContainer calls
	copyCalls  int        // counter for CopyToContainer calls
	copyDst    string     // last CopyToContainer destination path
	copyBytes  int64      // total bytes drained from CopyToContainer streams
	lastImage  string     // image of the most recent CreateContainer call
	lastEnv    map[string]string
	// stopKeepsRunning models a container the daemon accepts a stop for but
	// which is still running while postgres shuts down.
	stopKeepsRunning bool
}

func newMockDocker() *mockDockerClient {
	return &mockDockerClient{containers: make(map[string]string)}
}

func (m *mockDockerClient) CreateContainer(_ context.Context, name, image string, env map[string]string, ports map[string]string) (string, error) {
	if m.failOn == "create" {
		return "", fmt.Errorf("create failed")
	}
	m.lastImage = image
	m.lastEnv = env
	id := "container-" + name
	m.containers[id] = "created"
	return id, nil
}

func (m *mockDockerClient) StartContainer(_ context.Context, containerID string) error {
	if m.failOn == "start" {
		return fmt.Errorf("start failed")
	}
	m.containers[containerID] = "running"
	m.starts++
	return nil
}

func (m *mockDockerClient) StopContainer(_ context.Context, containerID string) error {
	if m.failOn == "stop" {
		return fmt.Errorf("stop failed")
	}
	if !m.stopKeepsRunning {
		m.containers[containerID] = "stopped"
	}
	m.stops++
	return nil
}

func (m *mockDockerClient) RemoveContainer(_ context.Context, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

func (m *mockDockerClient) ContainerStatus(_ context.Context, containerID string) (string, error) {
	if m.failOn == "status" {
		return "", fmt.Errorf("daemon unreachable")
	}
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
	m.execLog = append(m.execLog, cmd)
	if m.failOn == "exec" {
		return 2, nil // pg_isready exit 2 = no connection attempt
	}
	return 0, nil
}

func (m *mockDockerClient) CopyToContainer(_ context.Context, _ string, dstPath string, content io.Reader) error {
	m.copyCalls++
	m.copyDst = dstPath
	if m.failOn == "copy" {
		return fmt.Errorf("copy failed")
	}
	n, _ := io.Copy(io.Discard, content)
	m.copyBytes += n
	return nil
}

func (m *mockDockerClient) CopyFromContainer(_ context.Context, _ string, _ string) (io.ReadCloser, error) {
	if m.failOn == "copyFrom" {
		return nil, fmt.Errorf("copy from failed")
	}
	return io.NopCloser(strings.NewReader("")), nil
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

	req := domain.ProvisioningRequest{ProjectName: "my-app", DBType: domain.PostgreSQL, PostgresVersion: "17"}
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

func TestDockerProvisioner_EachProjectGetsItsOwnRandomSuperuserPassword(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)
	seen := map[string]bool{}
	for _, name := range []string{"one", "two", "three"} {
		req := domain.ProvisioningRequest{ProjectName: name, DBType: domain.PostgreSQL, PostgresVersion: "17"}
		result, err := p.Provision(context.Background(), req, config.TierConfig{}, func(domain.ProvisioningStage) {})
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		password := docker.lastEnv["POSTGRES_PASSWORD"]
		if password != result.Password {
			t.Error("the container's superuser password must be the one handed back for the vault")
		}
		if len(password) < 32 {
			t.Errorf("superuser password is %d chars, want at least 32", len(password))
		}
		if seen[password] {
			t.Fatal("two projects were given the same superuser password")
		}
		seen[password] = true
	}
}

func TestDockerProvisioner_Deprovision(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)

	// Provision first
	req := domain.ProvisioningRequest{ProjectName: "to-delete", DBType: domain.PostgreSQL, PostgresVersion: "17"}
	result, _ := p.Provision(context.Background(), req, config.TierConfig{}, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })

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

	req := domain.ProvisioningRequest{ProjectName: "status-test", DBType: domain.PostgreSQL, PostgresVersion: "17"}
	result, _ := p.Provision(context.Background(), req, config.TierConfig{}, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })

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

	_, err := p.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "fail", PostgresVersion: "17"}, config.TierConfig{}, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })
	if err == nil {
		t.Error("expected error when create fails")
	}
}

func TestDockerProvisioner_HealthCheckFails(t *testing.T) {
	docker := newMockDocker()
	docker.failOn = "health"
	p := NewDockerPostgreSQLProvisioner(docker)

	_, err := p.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "unhealthy", PostgresVersion: "17"}, config.TierConfig{}, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })
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

	_, err := p.Provision(ctx, domain.ProvisioningRequest{ProjectName: "stuck", PostgresVersion: "17"}, config.TierConfig{}, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })
	if err == nil {
		t.Error("expected error when pg_isready never returns 0")
	}
}

func TestDockerProvisioner_ConfigureArchive_RunsAlterSystemThenRestarts(t *testing.T) {
	docker := newMockDocker()
	docker.containers["container-pg"] = "running"
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.ConfigureArchive(context.Background(), "container-pg", "wal-g wal-push %p", "postgres"); err != nil {
		t.Fatalf("ConfigureArchive: %v", err)
	}

	// Expect: ALTER SYSTEM (archive_mode + archive_command + wal_level)
	// then a stop + start. Order matters: ALTER SYSTEM persists to
	// postgresql.auto.conf; the restart picks it up. Without the
	// restart, archive_mode is silently ignored.
	if len(docker.execLog) < 1 {
		t.Fatalf("expected 1+ execs, got %d: %v", len(docker.execLog), docker.execLog)
	}
	hasArchiveMode := false
	hasArchiveCmd := false
	hasWalLevel := false
	for _, cmd := range docker.execLog {
		joined := fmt.Sprintf("%v", cmd)
		if contains(joined, "archive_mode") {
			hasArchiveMode = true
		}
		if contains(joined, "archive_command") {
			hasArchiveCmd = true
		}
		if contains(joined, "wal_level") {
			hasWalLevel = true
		}
	}
	if !hasArchiveMode || !hasArchiveCmd || !hasWalLevel {
		t.Errorf("missing ALTER SYSTEM call(s): mode=%v cmd=%v level=%v", hasArchiveMode, hasArchiveCmd, hasWalLevel)
	}
	if docker.stops != 1 || docker.starts != 1 {
		t.Errorf("expected 1 stop + 1 start after ALTER SYSTEM, got stops=%d starts=%d", docker.stops, docker.starts)
	}
	if docker.containers["container-pg"] != "running" {
		t.Errorf("container should be running after restart, got %q", docker.containers["container-pg"])
	}
}

func TestDockerProvisioner_ConfigureArchive_RestartsLast(t *testing.T) {
	// Ordering test: the restart must happen AFTER the ALTER SYSTEM
	// commands. If it ran before, archive_mode would still be off.
	docker := &orderedMockDocker{mockDockerClient: newMockDocker()}
	docker.containers["c1"] = "running"
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.ConfigureArchive(context.Background(), "c1", "cp %p /walarchive/%f", "postgres"); err != nil {
		t.Fatalf("ConfigureArchive: %v", err)
	}
	// Expect events in this order: 3+ Execs, then Stop, then Start.
	stopIdx, startIdx := -1, -1
	lastExecIdx := -1
	for i, ev := range docker.events {
		switch ev {
		case "exec":
			lastExecIdx = i
		case "stop":
			stopIdx = i
		case "start":
			startIdx = i
		}
	}
	if stopIdx < lastExecIdx {
		t.Errorf("Stop ran before last Exec (stopIdx=%d, lastExecIdx=%d)", stopIdx, lastExecIdx)
	}
	if startIdx < stopIdx {
		t.Errorf("Start ran before Stop (startIdx=%d, stopIdx=%d)", startIdx, stopIdx)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || (len(haystack) > 0 && indexOf(haystack, needle) >= 0))
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// orderedMockDocker tracks event ordering for the restart-after-alter test.
type orderedMockDocker struct {
	*mockDockerClient
	events []string
}

func (m *orderedMockDocker) ExecInContainer(ctx context.Context, id string, cmd []string) (int, error) {
	m.events = append(m.events, "exec")
	return m.mockDockerClient.ExecInContainer(ctx, id, cmd)
}
func (m *orderedMockDocker) StopContainer(ctx context.Context, id string) error {
	m.events = append(m.events, "stop")
	return m.mockDockerClient.StopContainer(ctx, id)
}
func (m *orderedMockDocker) StartContainer(ctx context.Context, id string) error {
	m.events = append(m.events, "start")
	return m.mockDockerClient.StartContainer(ctx, id)
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

func TestFactory_RegisteredAndGet(t *testing.T) {
	docker := newMockDocker()
	dp := NewDockerPostgreSQLProvisioner(docker)
	factory := NewFactory(dp)

	reg := factory.Registered()
	if len(reg) != 1 || reg[0] != dp {
		t.Errorf("Registered: got %v, want [dp]", reg)
	}

	// Unknown type is not registered.
	if _, ok := factory.Get(domain.DatabaseType("nope")); ok {
		t.Error("unknown db type should not resolve")
	}

	// Empty factory has no registrations.
	if len(NewFactory().Registered()) != 0 {
		t.Error("empty factory should have zero registrations")
	}
}

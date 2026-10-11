package service

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-592: MySQL projects on a single Docker or Podman host.

// hostEngine is a container engine in memory: names are ids, every exec
// answers, and what is fed on stdin is kept.
type hostEngine struct {
	mu         sync.Mutex
	containers map[string]bool // name → running
	specs      map[string]provisioner.ContainerSpec
	stdin      []string
	// answer replies to a statement batch fed on stdin.
	answer func(container, stdin string) (string, error)
	copied map[string][]string // container → tar entry names copied in
	files  map[string][]byte   // container:path → bytes a CopyFromContainer returns
}

func newHostEngine() *hostEngine {
	return &hostEngine{
		containers: map[string]bool{}, specs: map[string]provisioner.ContainerSpec{},
		copied: map[string][]string{}, files: map[string][]byte{},
	}
}

func (h *hostEngine) CreateContainer(ctx context.Context, name, image string, env, ports map[string]string, limits provisioner.ContainerLimits) (string, error) {
	return h.CreateContainerSpec(ctx, provisioner.ContainerSpec{Name: name, Image: image, Env: env, Ports: ports, Limits: limits})
}

func (h *hostEngine) CreateContainerSpec(_ context.Context, spec provisioner.ContainerSpec) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, taken := h.containers[spec.Name]; taken {
		return "", fmt.Errorf("container name %s is in use", spec.Name)
	}
	h.containers[spec.Name] = false
	h.specs[spec.Name] = spec
	return spec.Name, nil
}

func (h *hostEngine) StartContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.containers[id]; !ok {
		return fmt.Errorf("no container %s", id)
	}
	h.containers[id] = true
	return nil
}

func (h *hostEngine) StopContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.containers[id]; ok {
		h.containers[id] = false
	}
	return nil
}

func (h *hostEngine) RemoveContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.containers, id)
	return nil
}

func (h *hostEngine) ContainerStatus(_ context.Context, id string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	running, ok := h.containers[id]
	switch {
	case !ok:
		return "not_found", nil
	case running:
		return "running", nil
	}
	return "stopped", nil
}

func (h *hostEngine) ContainerState(_ context.Context, id string) (provisioner.ContainerState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	running, ok := h.containers[id]
	if !ok {
		return provisioner.ContainerState{}, nil
	}
	ports := map[string]int{}
	if running {
		ports["3306"] = 43306
	}
	return provisioner.ContainerState{Found: true, Running: running, HostPorts: ports}, nil
}

func (h *hostEngine) WaitForHealthy(context.Context, string) error { return nil }

func (h *hostEngine) ExecInContainer(context.Context, string, []string) (int, error) { return 0, nil }

func (h *hostEngine) ExecInContainerStdin(_ context.Context, id string, _ []string, stdin string) (string, error) {
	h.mu.Lock()
	h.stdin = append(h.stdin, stdin)
	answer := h.answer
	h.mu.Unlock()
	if answer != nil {
		return answer(id, stdin)
	}
	return "", nil
}

func (h *hostEngine) CopyToContainer(_ context.Context, id, _ string, content io.Reader) error {
	names, err := tarNames(content)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.copied[id] = append(h.copied[id], names...)
	return nil
}

func (h *hostEngine) CopyFromContainer(_ context.Context, id, src string) (io.ReadCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, ok := h.files[id+":"+src]
	if !ok {
		return nil, fmt.Errorf("no %s in %s", src, id)
	}
	return io.NopCloser(tarOf(src[strings.LastIndex(src, "/")+1:], body)), nil
}

func (h *hostEngine) fed() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.stdin, "\n")
}

func setupSingleHostMySQL(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *hostEngine) {
	t.Helper()
	svc, store, _ := setupProvisioningTest(t)
	engine := newHostEngine()
	mysql := provisioner.NewDockerMySQLProvisioner(engine)
	poll := provisioner.NewPoller(time.Millisecond, 100*time.Millisecond)
	mysql.SetPollers(poll, poll)
	svc.factory = provisioner.NewFactory(provisioner.NewDockerPostgreSQLProvisioner(engine), mysql)
	svc.SetDefaultDeploymentMode(domain.ModeDocker)
	svc.SetDockerClient(engine)
	svc.AllowMySQLCreation(true)
	svc.SetBackgroundRunner(func(run func()) { run() })
	return svc, store, engine
}

func TestSingleHostMySQLProjectFilesItsPlatformLogins(t *testing.T) {
	svc, store, engine := setupSingleHostMySQL(t)
	inst := provisionMySQL(t, svc, store, "")
	container := "excalibase-" + inst.ProjectID + "-mysql"
	if inst.DeploymentMode != domain.ModeDocker || inst.Namespace != container || inst.Host != container ||
		inst.Port == nil || *inst.Port != 3306 || inst.SSLMode != "REQUIRED" {
		t.Fatalf("row %+v", inst)
	}
	if !engine.containers[container] {
		t.Fatal("the project's container is not running")
	}
	if inst.BackupEnabled == nil || !*inst.BackupEnabled || inst.BackupSchedule != "0 */6 * * *" {
		t.Fatalf("a Free project is backed up every 6 hours by the platform: %v %q", inst.BackupEnabled, inst.BackupSchedule)
	}
	created := engine.fed()
	for _, role := range []string{roleAdmin, roleApp, roleAuthAdmin, roleWatcher} {
		if !strings.Contains(created, "CREATE USER IF NOT EXISTS '"+role+"'@'%'") {
			t.Errorf("%s was not created", role)
		}
		record, err := svc.vault.Get(vaultCredentialPath(inst.ProjectID, role))
		if err != nil || record["engine"] != "mysql" || record["host"] != container || record["port"] != "3306" {
			t.Errorf("%s record %v %v", role, record, err)
		}
	}
}

func TestSingleHostMySQLRotationAndReappliedPasswordsGoToTheContainer(t *testing.T) {
	svc, store, engine := setupSingleHostMySQL(t)
	svc.SetCredentialVerifier(&fakeRoleVerifier{})
	inst := provisionMySQL(t, svc, store, "")
	before := len(engine.stdin)
	if _, err := svc.RotateCredentials(context.Background(), inst.ProjectID); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	record, _ := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleAdmin))
	if !strings.Contains(strings.Join(engine.stdin[before:], "\n"), "ALTER USER 'admin'@'%' IDENTIFIED BY '"+record["password"]+"'") {
		t.Fatal("the rotated admin password was not set in the container")
	}
	before = len(engine.stdin)
	if err := svc.ReapplyCredentials(context.Background(), inst); err != nil {
		t.Fatalf("ReapplyCredentials: %v", err)
	}
	if !strings.Contains(strings.Join(engine.stdin[before:], "\n"), "ALTER USER 'admin'@'%' IDENTIFIED BY '"+record["password"]+"'") {
		t.Fatal("the filed admin password was not put back")
	}
}

// tarNames lists the entries of a tar stream.
func tarNames(content io.Reader) ([]string, error) {
	reader := tar.NewReader(content)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, header.Name)
	}
}

// tarOf is a one-file tar stream, as the engine answers a copy out.
func tarOf(name string, body []byte) io.Reader {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	_ = writer.WriteHeader(&tar.Header{Name: name, Mode: 0o640, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = writer.Write(body)
	_ = writer.Close()
	return &buffer
}

func TestSingleHostMySQLIsTheInstallationsMySQLServer(t *testing.T) {
	svc, _, _ := setupSingleHostMySQL(t)
	if svc.SingleHostMySQL() == nil || svc.MySQLWorkReader() == nil {
		t.Fatal("a single host serves MySQL through its container provisioner")
	}
	if NewMySQLEngineActivity(svc.MySQLWorkReader()) == nil {
		t.Fatal("no activity reader")
	}
	k8s, _, _ := setupMySQLTest(t)
	if k8s.SingleHostMySQL() != nil || k8s.MySQLWorkReader() == nil {
		t.Fatal("Kubernetes serves MySQL through MOCO")
	}
}

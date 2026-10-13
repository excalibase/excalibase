package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testWatcherImage = "docker.io/excalibase/excalibase-watcher-go@sha256:7b2f9feb21b2a92b34c3f7b136399c619d460bf5d296215b8662a7a334126d9e"

func watcherProvisioner(engine *fakeEngine) *DockerPostgreSQLProvisioner {
	p := NewDockerPostgreSQLProvisioner(engine)
	p.SetWatcher(DockerWatcherConfig{Image: testWatcherImage, NatsURL: "nats://nats:4222", Network: "excalibase-cdc", Stream: "CDC"})
	return p
}

func dockerWatcherSpec() WatcherSpec {
	return WatcherSpec{ProjectID: "p1", DBName: "app", Host: "excalibase-p1-postgres", Port: 5432, Username: "cdc_watcher", Password: "s3cret'pass"}
}

// watcherConfig reads the config file the watcher was handed through its archive.
func watcherConfig(t *testing.T, engine *fakeEngine) map[string]map[string]any {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(engine.copied["excalibase-p1-watcher:/"]))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatal("no config file in the watcher's archive")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "etc/watcher/config.json" {
			continue
		}
		if header.Uid != watcherUID || header.Mode&0o077 != 0o040 && header.Mode&0o077 != 0 {
			t.Fatalf("config owned by %d mode %o", header.Uid, header.Mode)
		}
		var config map[string]map[string]any
		if err := json.NewDecoder(reader).Decode(&config); err != nil {
			t.Fatal(err)
		}
		return config
	}
}

func TestDeployWatcherRunsTheProjectsWatcherOnTheBusNetwork(t *testing.T) {
	engine := newFakeEngine()
	if err := watcherProvisioner(engine).DeployWatcher(context.Background(), dockerWatcherSpec()); err != nil {
		t.Fatal(err)
	}
	spec := engine.specs["excalibase-p1-watcher"]
	if spec.Image != testWatcherImage || !slices.Equal(spec.Networks, []string{"excalibase-cdc"}) || !spec.Hardened || spec.User != "65532:65532" {
		t.Fatalf("watcher spec %+v", spec)
	}
	if len(spec.Ports) != 0 || spec.Limits.MemoryBytes == 0 || spec.Limits.NanoCPUs == 0 {
		t.Fatalf("watcher must publish nothing and be limited: %+v", spec)
	}
	if _, inEnv := spec.Env["WATCHER_POSTGRES_PASSWORD"]; inEnv {
		t.Fatal("the password must stay out of the container's environment")
	}
	if !slices.Equal(spec.Cmd, []string{"--config", "/etc/watcher/config.json"}) {
		t.Fatalf("cmd %v", spec.Cmd)
	}
	if engine.indexOf("copy:excalibase-p1-watcher") > engine.indexOf("start:excalibase-p1-watcher") || engine.indexOf("start:excalibase-p1-watcher") < 0 {
		t.Fatalf("config must be in place before the start: %v", engine.calls)
	}
	config := watcherConfig(t, engine)
	pg, bus := config["postgres"], config["nats"]
	if pg["url"] != "postgres://excalibase-p1-postgres:5432/app?replication=database&sslmode=disable" ||
		pg["username"] != "cdc_watcher" || pg["password"] != "s3cret'pass" ||
		pg["slot_name"] != "cdc_watcher" || pg["publication_name"] != "cdc_watcher_pub" ||
		pg["create_slot"] != true || pg["create_publication"] != false {
		t.Fatalf("postgres config %v", pg)
	}
	if bus["url"] != "nats://nats:4222" || bus["stream_name"] != "CDC" || bus["subject_prefix"] != "cdc.p1" {
		t.Fatalf("nats config %v", bus)
	}
}

func TestDeployWatcherReplacesAPreviousWatcher(t *testing.T) {
	engine := newFakeEngine()
	p := watcherProvisioner(engine)
	for range 2 {
		if err := p.DeployWatcher(context.Background(), dockerWatcherSpec()); err != nil {
			t.Fatal(err)
		}
	}
	if engine.indexOf("remove:excalibase-p1-watcher") < 0 || !engine.containers["excalibase-p1-watcher"].running {
		t.Fatalf("calls %v", engine.calls)
	}
}

func TestDeployWatcherRefusesAnIncompleteSpec(t *testing.T) {
	spec := dockerWatcherSpec()
	spec.Password = ""
	if err := watcherProvisioner(newFakeEngine()).DeployWatcher(context.Background(), spec); err == nil {
		t.Fatal("a watcher without its password must not be started")
	}
}

func TestDeployWatcherDoesNothingWhereNoWatcherIsConfigured(t *testing.T) {
	engine := newFakeEngine()
	if err := NewDockerPostgreSQLProvisioner(engine).DeployWatcher(context.Background(), dockerWatcherSpec()); err != nil {
		t.Fatal(err)
	}
	if len(engine.calls) != 0 {
		t.Fatalf("calls %v", engine.calls)
	}
}

func TestStopReplicationStopsTheWatcher(t *testing.T) {
	engine := newFakeEngine()
	p := watcherProvisioner(engine)
	if err := p.StopReplication(context.Background(), "excalibase-p1-postgres", "p1"); err != nil {
		t.Fatalf("no watcher yet: %v", err)
	}
	if err := p.DeployWatcher(context.Background(), dockerWatcherSpec()); err != nil {
		t.Fatal(err)
	}
	if err := p.StopReplication(context.Background(), "excalibase-p1-postgres", "p1"); err != nil {
		t.Fatal(err)
	}
	if engine.containers["excalibase-p1-watcher"].running {
		t.Fatal("watcher still running")
	}
}

func TestDeprovisionRemovesTheWatcherBeforeTheDatabase(t *testing.T) {
	engine := newFakeEngine()
	p := watcherProvisioner(engine)
	engine.containers["excalibase-p1-postgres"] = &fakeContainer{running: true}
	if err := p.DeployWatcher(context.Background(), dockerWatcherSpec()); err != nil {
		t.Fatal(err)
	}
	if err := p.Deprovision(context.Background(), "excalibase-p1-postgres", "p1"); err != nil {
		t.Fatal(err)
	}
	watcher, database := engine.indexOf("remove:excalibase-p1-watcher"), engine.indexOf("remove:excalibase-p1-postgres")
	if watcher < 0 || database < 0 || watcher > database {
		t.Fatalf("calls %v", engine.calls)
	}
}

func TestProvisionStartsPostgresDecodingLogicallyWithItsSlotWALCapped(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: "live", DBType: domain.PostgreSQL, PostgresVersion: "17"}
	tier := config.TierConfig{Memory: "512Mi", CPU: "0.5", StorageSize: "10Gi"}
	if _, err := p.Provision(context.Background(), req, tier, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatal(err)
	}
	want := []string{"postgres", "-c", "wal_level=logical", "-c", "max_slot_wal_keep_size=2048MB"}
	if !slices.Equal(docker.lastCmd, want) {
		t.Fatalf("postgres command %v, want %v", docker.lastCmd, want)
	}
}

func TestArchivingKeepsLogicalDecoding(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	p := NewDockerPostgreSQLProvisioner(docker)
	if err := p.ConfigureArchive(context.Background(), "c1", "true", "postgres"); err != nil {
		t.Fatal(err)
	}
	joined := fmt.Sprint(docker.execLog)
	if !strings.Contains(joined, "wal_level = 'logical'") || strings.Contains(joined, "'replica'") {
		t.Fatalf("archive settings %s", joined)
	}
}

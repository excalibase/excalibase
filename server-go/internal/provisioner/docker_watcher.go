package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"time"
)

// A single host runs each Postgres project's CDC watcher as a container of its
// own, as Kubernetes runs it as a pod (EXC-612): it reads the project's
// replication slot over the tenants network and publishes to NATS over the bus
// network, which the engine proxy admits for the watcher image alone.

// DockerWatcherConfig is what every project's watcher shares. An empty Image
// runs no watcher.
type DockerWatcherConfig struct {
	// Image is the digest-pinned watcher image the engine proxy admits to Network.
	Image   string
	NatsURL string
	// Network is the bus network NATS listens on.
	Network string
	Stream  string
}

const (
	watcherConfigDir  = "etc/watcher"
	watcherConfigPath = "/" + watcherConfigDir + "/config.json"
	// watcherUID is the distroless image's nonroot user, which reads the config.
	watcherUID  = 65532
	watcherUser = "65532:65532"
)

// The watcher's size on Kubernetes: a limit of 256Mi and 200m.
var watcherLimits = ContainerLimits{MemoryBytes: 256 << 20, NanoCPUs: 200_000_000}

var errWatcherSpecIncomplete = errors.New("watcher spec needs the project, database, host and cdc_watcher login")

// SetWatcher configures the watcher every project runs.
func (p *DockerPostgreSQLProvisioner) SetWatcher(config DockerWatcherConfig) { p.watcher = config }

// WatcherContainerName is the project's watcher container.
func WatcherContainerName(projectID string) string {
	return fmt.Sprintf("excalibase-%s-watcher", projectID)
}

// DeployWatcher (re)creates the project's watcher and starts it. A previous
// watcher is removed first, so a resume or a restore starts from the
// credentials just read and the slot is taken over, never duplicated.
func (p *DockerPostgreSQLProvisioner) DeployWatcher(ctx context.Context, spec WatcherSpec) error {
	if p.watcher.Image == "" {
		return nil
	}
	if spec.ProjectID == "" || spec.DBName == "" || spec.Host == "" || spec.Username == "" || spec.Password == "" {
		return errWatcherSpecIncomplete
	}
	config, err := watcherArchive(p.watcher, spec)
	if err != nil {
		return err
	}
	name := WatcherContainerName(spec.ProjectID)
	if err := p.removeObserved(ctx, name); err != nil {
		return fmt.Errorf("remove the previous watcher: %w", err)
	}
	id, err := p.docker.CreateContainerSpec(ctx, ContainerSpec{
		Name:     name,
		Image:    p.watcher.Image,
		Cmd:      []string{"--config", watcherConfigPath},
		Limits:   watcherLimits,
		Networks: []string{p.watcher.Network},
		Hardened: true,
		User:     watcherUser,
	})
	if err != nil {
		return fmt.Errorf("create the watcher: %w", err)
	}
	if err := p.docker.CopyToContainer(ctx, id, "/", bytes.NewReader(config)); err != nil {
		return fmt.Errorf("hand the watcher its config: %w", err)
	}
	if err := p.docker.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("start the watcher: %w", err)
	}
	log.Printf("watcher %s started for %s", name, spec.ProjectID)
	return nil
}

// StopReplication stops the project's watcher, so its replication session
// does not hold the database's shutdown open. A project without one is fine.
func (p *DockerPostgreSQLProvisioner) StopReplication(ctx context.Context, _, projectID string) error {
	name := WatcherContainerName(projectID)
	status, err := p.docker.ContainerStatus(ctx, name)
	if err != nil {
		return fmt.Errorf("watcher status: %w", err)
	}
	if status == containerNotFound {
		return nil
	}
	if err := p.docker.StopContainer(ctx, name); err != nil {
		return fmt.Errorf("stop the watcher: %w", err)
	}
	return nil
}

// watcherArchive is the tar holding the watcher's config, readable by the
// watcher's user alone. The cdc_watcher password lives in this file, not in
// the container's environment, which any inspect prints.
func watcherArchive(watcher DockerWatcherConfig, spec WatcherSpec) ([]byte, error) {
	config, err := json.Marshal(watcherConfigFile(watcher, spec))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	now := time.Now()
	headers := []*tar.Header{
		{Name: watcherConfigDir + "/", Typeflag: tar.TypeDir, Mode: 0o500, Uid: watcherUID, Gid: watcherUID, ModTime: now},
		{Name: watcherConfigDir + "/config.json", Typeflag: tar.TypeReg, Mode: 0o400, Uid: watcherUID, Gid: watcherUID, ModTime: now, Size: int64(len(config))},
	}
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
	}
	if _, err := writer.Write(config); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// watcherConfigFile mirrors the values Kubernetes hands the watcher chart.
func watcherConfigFile(watcher DockerWatcherConfig, spec WatcherSpec) map[string]any {
	port := spec.Port
	if port == 0 {
		port = 5432
	}
	target := url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(spec.Host, strconv.Itoa(port)),
		Path:     "/" + spec.DBName,
		RawQuery: url.Values{"sslmode": {"disable"}, "replication": {"database"}}.Encode(),
	}
	return map[string]any{
		"postgres": map[string]any{
			"enabled":            true,
			"url":                target.String(),
			"username":           spec.Username,
			"password":           spec.Password,
			"slot_name":          "cdc_watcher",
			"publication_name":   "cdc_watcher_pub",
			"create_slot":        true,
			"create_publication": false,
			"capture_ddl":        false,
		},
		"nats": map[string]any{
			"enabled":        true,
			"url":            watcher.NatsURL,
			"stream_name":    watcher.Stream,
			"subject_prefix": "cdc." + spec.ProjectID,
		},
		"health":  map[string]any{"port": 8080, "bind_address": "127.0.0.1"},
		"metrics": map[string]any{"enabled": true},
	}
}

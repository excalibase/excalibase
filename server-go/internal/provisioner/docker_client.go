package provisioner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DockerClientOptions configures how NewRealDockerClient reaches the daemon.
// All fields are optional; when every field is zero the constructor falls
// through to DOCKER_HOST env then to /var/run/docker.sock.
type DockerClientOptions struct {
	// Host is an explicit URI, e.g. "unix:///var/run/docker.sock" or
	// "tcp://docker.example.com:2376". Takes priority over env lookup.
	Host string
	// CertPath is a directory containing ca.pem, cert.pem, key.pem. If set
	// alongside Host, TLS is enabled.
	CertPath string
	// TLSVerify is kept for symmetry with DOCKER_TLS_VERIFY but has no
	// effect unless CertPath is also set.
	TLSVerify bool
	// BindAddress is the host IP that provisioned DB ports are published on.
	// Empty → 127.0.0.1 (loopback, internal-only). Set "0.0.0.0" to expose on
	// the host's network interface (DOCKER_DB_PUBLIC=true).
	BindAddress string
	// Network is a user-defined docker network to attach provisioned DB
	// containers to, so graphql/schema can resolve them by name. Empty →
	// default bridge (no name resolution across containers).
	Network string
}

// RealDockerClient implements DockerClient against a real Docker daemon via
// the official SDK. Satisfies the same interface the mock uses in tests.
type RealDockerClient struct {
	c        *client.Client
	bindAddr string // host IP for published DB ports; "" → 127.0.0.1
	netName  string // user-defined network to attach DB containers to; "" → bridge
}

// RawClient returns the underlying SDK client. Used by the backup
// service to share the same Docker connection for `docker exec`
// streaming without re-doing the daemon discovery / TLS handshake.
func (r *RealDockerClient) RawClient() *client.Client { return r.c }

// NewRealDockerClient builds a client using the priority chain:
//
//  1. opts.Host (explicit) — with optional TLS from opts.CertPath
//  2. DOCKER_HOST env (via client.FromEnv) — also reads DOCKER_TLS_VERIFY,
//     DOCKER_CERT_PATH, DOCKER_API_VERSION
//  3. unix:///var/run/docker.sock — local-host fallback
//
// A Ping() is performed at startup so mis-configured connections fail fast
// instead of only surfacing during the first provisioning run.
func NewRealDockerClient(opts DockerClientOptions) (*RealDockerClient, error) {
	var clientOpts []client.Opt

	switch {
	case opts.Host != "":
		clientOpts = append(clientOpts, client.WithHost(opts.Host))
		if opts.CertPath != "" {
			clientOpts = append(clientOpts, client.WithTLSClientConfig(
				filepath.Join(opts.CertPath, "ca.pem"),
				filepath.Join(opts.CertPath, "cert.pem"),
				filepath.Join(opts.CertPath, "key.pem"),
			))
		}
	case os.Getenv("DOCKER_HOST") != "":
		clientOpts = append(clientOpts, client.FromEnv)
	default:
		clientOpts = append(clientOpts, client.WithHost("unix:///var/run/docker.sock"))
	}

	clientOpts = append(clientOpts, client.WithAPIVersionNegotiation())

	c, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("init docker client: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Ping(pingCtx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping docker daemon: %w", err)
	}

	return &RealDockerClient{c: c, bindAddr: opts.BindAddress, netName: opts.Network}, nil
}

// excalibaseLabel marks every container created by the provisioner so
// operators can list/clean them with `docker ps -f label=excalibase.managed`.
const excalibaseLabel = "excalibase.managed"

// Close releases the underlying docker client connection.
func (r *RealDockerClient) Close() error {
	if r.c == nil {
		return nil
	}
	return r.c.Close()
}

// portBindings maps container→host ports, publishing each on hostIP. An empty
// hostIP defaults to 127.0.0.1 (loopback) so provisioned DBs are not exposed on
// the host's LAN/public interface unless the operator explicitly opts into
// 0.0.0.0 (DOCKER_DB_PUBLIC=true).
func portBindings(ports map[string]string, hostIP string) (nat.PortSet, nat.PortMap, error) {
	if hostIP == "" {
		hostIP = "127.0.0.1"
	}
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for containerPort, hostPort := range ports {
		np, err := nat.NewPort("tcp", containerPort)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid container port %q: %w", containerPort, err)
		}
		exposed[np] = struct{}{}
		bindings[np] = []nat.PortBinding{{HostIP: hostIP, HostPort: hostPort}}
	}
	return exposed, bindings, nil
}

// networkingConfig attaches the container to a user-defined network so
// consumers can resolve it by name (tenant DB creds are stored as
// containerName:5432). Empty netName → no endpoints (default bridge).
func networkingConfig(netName string) *network.NetworkingConfig {
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if netName != "" {
		nc.EndpointsConfig[netName] = &network.EndpointSettings{}
	}
	return nc
}

// CreateContainer creates a container with the given env + port bindings and
// returns its ID. Does not start the container. ports maps "containerPort" →
// "hostPort" (empty hostPort = random free port).
func (r *RealDockerClient) CreateContainer(ctx context.Context, name, img string, env, ports map[string]string) (string, error) {
	if err := r.ensureImage(ctx, img); err != nil {
		return "", err
	}

	envSlice := make([]string, 0, len(env))
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	exposed, bindings, err := portBindings(ports, r.bindAddr)
	if err != nil {
		return "", err
	}

	cfg := &container.Config{
		Image:        img,
		Env:          envSlice,
		ExposedPorts: exposed,
		Labels:       map[string]string{excalibaseLabel: "true"},
	}
	hostCfg := &container.HostConfig{
		PortBindings:  bindings,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
	}

	resp, err := r.c.ContainerCreate(ctx, cfg, hostCfg, networkingConfig(r.netName), nil, name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

func (r *RealDockerClient) StartContainer(ctx context.Context, id string) error {
	if err := r.c.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	return nil
}

// StopContainer tries to stop gracefully (30s) then lets Docker kill. Missing
// containers are treated as success to keep Deprovision idempotent.
func (r *RealDockerClient) StopContainer(ctx context.Context, id string) error {
	timeout := 30
	err := r.c.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// RemoveContainer force-removes the container and its volumes. Missing
// containers are treated as success for idempotency.
func (r *RealDockerClient) RemoveContainer(ctx context.Context, id string) error {
	err := r.c.ContainerRemove(ctx, id, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// ContainerStatus reports "running", "stopped", or "not_found". Matches the
// coarse-grained vocabulary the mock uses in tests.
func (r *RealDockerClient) ContainerStatus(ctx context.Context, id string) (string, error) {
	insp, err := r.c.ContainerInspect(ctx, id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return "not_found", nil
		}
		return "", fmt.Errorf("inspect container %s: %w", id, err)
	}
	if insp.State != nil && insp.State.Running {
		return "running", nil
	}
	return "stopped", nil
}

// WaitForHealthy polls ContainerInspect until the container is running and
// (if a healthcheck is defined) reports "healthy". Caps at 60 seconds; the
// provisioning pipeline should not block indefinitely on a stuck container.
func (r *RealDockerClient) WaitForHealthy(ctx context.Context, id string) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		insp, err := r.c.ContainerInspect(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect container %s: %w", id, err)
		}
		if insp.State == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if !insp.State.Running {
			return fmt.Errorf("container %s exited: status=%s exitCode=%d error=%s",
				id, insp.State.Status, insp.State.ExitCode, insp.State.Error)
		}
		if insp.State.Health != nil {
			switch insp.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return fmt.Errorf("container %s health check failed", id)
			}
			// "starting" or "" → keep polling
		} else {
			// No health check — "running" is enough.
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("container %s did not become healthy within 60s", id)
}

// ExecInContainer runs cmd inside a running container and returns its
// exit code. Does not capture stdout/stderr — callers use it as a boolean
// probe (e.g. `pg_isready -U postgres`). Polls ContainerExecInspect for
// up to 10s waiting for the exec to finish.
func (r *RealDockerClient) ExecInContainer(ctx context.Context, id string, cmd []string) (int, error) {
	create, err := r.c.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: false,
		AttachStderr: false,
	})
	if err != nil {
		return 0, fmt.Errorf("exec create: %w", err)
	}
	if err := r.c.ContainerExecStart(ctx, create.ID, container.ExecStartOptions{Detach: false}); err != nil {
		return 0, fmt.Errorf("exec start: %w", err)
	}

	// Poll until the exec completes. Each probe is short-lived (pg_isready
	// returns in milliseconds) so a tight loop is fine.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		inspect, err := r.c.ContainerExecInspect(ctx, create.ID)
		if err != nil {
			return 0, fmt.Errorf("exec inspect: %w", err)
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("exec timeout waiting for command to finish")
}

// CopyToContainer streams a tar archive into the container's filesystem
// at dstPath. Wraps the SDK's CopyToContainer (a.k.a. PutArchive). The
// stream MUST be raw tar — Docker's API doesn't gunzip server-side.
// Used by the backup adapter's restore path to extract a downloaded
// pg_basebackup into /var/lib/postgresql/data before pg starts.
func (r *RealDockerClient) CopyToContainer(ctx context.Context, id, dstPath string, content io.Reader) error {
	if err := r.c.CopyToContainer(ctx, id, dstPath, content, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to %s:%s: %w", id, dstPath, err)
	}
	return nil
}

// CopyFromContainer wraps the SDK's CopyFromContainer (a.k.a.
// GetArchive). Returns a tar stream of srcPath. Caller must close the
// returned ReadCloser.
func (r *RealDockerClient) CopyFromContainer(ctx context.Context, id, srcPath string) (io.ReadCloser, error) {
	body, _, err := r.c.CopyFromContainer(ctx, id, srcPath)
	if err != nil {
		return nil, fmt.Errorf("copy from %s:%s: %w", id, srcPath, err)
	}
	return body, nil
}

// ensureImage skips the pull if the image already exists locally. Large
// images can take minutes to pull and the check is cheap.
func (r *RealDockerClient) ensureImage(ctx context.Context, ref string) error {
	args := filters.NewArgs()
	args.Add("reference", ref)
	summaries, err := r.c.ImageList(ctx, image.ListOptions{Filters: args})
	if err == nil && len(summaries) > 0 {
		return nil
	}

	reader, err := r.c.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer reader.Close()
	// Drain the stream. Abandoning mid-pull leaves a partial image on disk.
	if _, err := io.Copy(io.Discard, reader); err != nil && !strings.Contains(err.Error(), "context canceled") {
		return fmt.Errorf("drain pull stream: %w", err)
	}
	return nil
}

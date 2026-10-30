//go:build integration

package engineproxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// EXC-681 on the real engine: a mount whose type is given twice, in two cases, is read by the
// engine as the second one. The proxy refuses it and still forwards what provisioning sends.
//
// Run: go test -tags=integration ./internal/engineproxy/ -run TestRealEngine -v

const itBusybox = "docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func engineSocket() string {
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return strings.TrimPrefix(client.DefaultDockerHost, "unix://")
}

func realEngine(t *testing.T) (*client.Client, *http.Client, string) {
	t.Helper()
	direct, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("no engine: %v", err)
	}
	if _, err := direct.Ping(context.Background()); err != nil {
		t.Skipf("no engine: %v", err)
	}
	stream, err := direct.ImagePull(context.Background(), itBusybox, image.PullOptions{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
	socket := engineSocket()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	policy := Policy{ManagedLabel: ManagedLabel, PortBindIPs: []string{"127.0.0.1"}, VolumePrefix: "excalibase-it681-",
		ContainerPrefix: "excalibase-it681-"}
	proxy := httptest.NewServer(NewHandler(policy, &url.URL{Scheme: "http", Host: "engine"}, transport))
	t.Cleanup(proxy.Close)
	return direct, &http.Client{Transport: transport}, proxy.URL
}

func remove(t *testing.T, engine *client.Client, name string) {
	t.Helper()
	t.Cleanup(func() {
		_ = engine.ContainerRemove(context.Background(), name, container.RemoveOptions{Force: true, RemoveVolumes: true})
	})
}

func twinMountBody() string {
	return `{"Image":"` + itBusybox + `","Cmd":["true"],"Labels":{"excalibase.managed":"true"},` +
		`"HostConfig":{"Mounts":[{"Type":"tmpfs","type":"bind","Source":"/etc","Target":"/hostetc"}]}}`
}

func postCreate(t *testing.T, httpClient *http.Client, base, name, body string) int {
	t.Helper()
	resp, err := httpClient.Post(base+"/v1.41/containers/create?name="+name, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestRealEngineReadsTheTwinTheProxyRefuses(t *testing.T) {
	direct, socketClient, proxy := realEngine(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	// Straight to the engine, the lower-case twin wins: a tmpfs becomes the host's /etc.
	bare := "excalibase-it681-bare-" + suffix
	remove(t, direct, bare)
	if status := postCreate(t, socketClient, "http://engine", bare, twinMountBody()); status != http.StatusCreated {
		t.Fatalf("engine create = %d", status)
	}
	inspect, err := direct.ContainerInspect(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if !mountsHostPath(inspect, "/etc") {
		t.Fatalf("engine did not read the twin; mounts %+v, binds %v", inspect.HostConfig.Mounts, inspect.HostConfig.Binds)
	}

	proxied := "excalibase-it681-proxied-" + suffix
	remove(t, direct, proxied)
	if status := postCreate(t, http.DefaultClient, proxy, proxied, twinMountBody()); status != http.StatusForbidden {
		t.Fatalf("proxied create = %d, want 403", status)
	}
	if _, err := direct.ContainerInspect(context.Background(), proxied); !errdefs.IsNotFound(err) {
		t.Fatalf("the refused container exists (%v)", err)
	}
}

// Docker reports a bind made through Mounts in HostConfig.Mounts, Podman in HostConfig.Binds.
func mountsHostPath(inspect container.InspectResponse, source string) bool {
	for _, made := range inspect.HostConfig.Mounts {
		if made.Type == mount.TypeBind && made.Source == source {
			return true
		}
	}
	for _, bind := range inspect.HostConfig.Binds {
		if strings.HasPrefix(bind, source+":") {
			return true
		}
	}
	return false
}

func TestRealEngineTakesTheSDKCreateThroughTheProxy(t *testing.T) {
	direct, _, proxy := realEngine(t)
	proxied, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(proxy, "http://")), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := "excalibase-it681-sdk-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	remove(t, direct, name)
	volume := name + "-data"
	t.Cleanup(func() { _ = direct.VolumeRemove(context.Background(), volume, true) })
	host := &container.HostConfig{
		Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/data", ReadOnly: true}},
		Tmpfs:        map[string]string{"/tmp": "rw,noexec,size=1m"},
		PortBindings: nat.PortMap{"8080/tcp": {{HostIP: "127.0.0.1"}}},
		LogConfig:    container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m"}},
		CapDrop:      []string{"ALL"},
		SecurityOpt:  []string{"no-new-privileges"},
	}
	config := &container.Config{Image: itBusybox, Cmd: []string{"sh", "-c", "echo a && echo b > /tmp/x"},
		Env: []string{"A=<b>&c"}, Labels: map[string]string{ManagedLabel: "true"}, ExposedPorts: nat.PortSet{"8080/tcp": {}}}
	if _, err := proxied.ContainerCreate(ctx, config, host, nil, nil, name); err != nil {
		t.Fatalf("SDK create through the proxy: %v", err)
	}
	inspect, err := direct.ContainerInspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if got := inspect.Config.Env; !slices.Contains(got, "A=<b>&c") {
		t.Fatalf("env changed on the way: %v", got)
	}
	if got := inspect.HostConfig.PortBindings["8080/tcp"]; len(got) != 1 || got[0].HostIP != "127.0.0.1" {
		t.Fatalf("port bindings %v", got)
	}
}

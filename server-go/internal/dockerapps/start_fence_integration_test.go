//go:build integration

package dockerapps

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fenceprobe"
)

// EXC-622 on a single host: the project network is made before the app's
// container starts, so nothing off it may answer at any moment, from the
// first instruction the app runs. START_FENCE_SANDBOX_RUNTIME names the
// engine's sandbox runtime (runsc) when it has one.
//
// Run: go test -tags=integration ./internal/dockerapps/ -run TestRealEngineStartFence -v
func TestRealEngineStartFence(t *testing.T) {
	for _, egress := range []bool{false, true} {
		name := "egress none"
		if egress {
			name = "egress internet"
		}
		t.Run(name, func(t *testing.T) { proveStartFence(t, egress) })
	}
}

const (
	fenceDBPort       = "5432"
	fenceNeighborPort = "8080"
	fenceSeconds      = "10"
)

type startFenceLab struct {
	rig     *realRig
	targets []fenceprobe.Target
}

func proveStartFence(t *testing.T, egress bool) {
	rig := newRealRigWith(t, func(options *Options) {
		options.Egress = egress
		options.SandboxRuntime = os.Getenv("START_FENCE_SANDBOX_RUNTIME")
	}, "httpd", "-f", "-p", fenceDBPort)
	// As the wiring does: Podman keeps a project bridge with a gateway off the others.
	podman, err := IsPodman(context.Background(), rig.direct)
	if err != nil {
		t.Fatal(err)
	}
	rig.rt.opts.Isolate = podman
	lab := &startFenceLab{rig: rig}
	lab.targets = lab.startTargets(t, egress)
	t.Run("control: every target answers a container off the project network", func(t *testing.T) {
		samples := lab.control(t)
		for _, target := range lab.targets {
			if target.Name != "metadata" && target.Name != "own-db" && fenceprobe.FirstOpen(samples, target.Name, 0) < 0 {
				t.Errorf("%s (%s) never answered the control; a refusal would prove nothing", target.Name, target.Addr)
			}
		}
	})
	samples := lab.deployProbe(t)
	for _, breach := range fenceprobe.Breaches(samples, lab.targets, 0) {
		t.Errorf("from the start: %s", breach)
	}
	if fenceprobe.FirstOpen(samples, "own-db", 0) < 0 {
		t.Error("the app never reached its own database, so the refusals prove nothing")
	}
}

// startTargets starts another project's service on a network of its own and
// a neighbour on the engine's default bridge, published on the host.
func (lab *startFenceLab) startTargets(t *testing.T, egress bool) []fenceprobe.Target {
	t.Helper()
	ctx := context.Background()
	other := lab.rig.prefix + "-other"
	if _, err := lab.rig.direct.NetworkCreate(ctx, other, network.CreateOptions{Driver: "bridge"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.rig.direct.NetworkRemove(context.Background(), other) })
	tenant := lab.startServer(t, lab.rig.prefix+"-tenant", other, nil)
	published := nat.PortMap{nat.Port(fenceNeighborPort + "/tcp"): {{HostIP: "0.0.0.0"}}}
	neighbor := lab.startServer(t, lab.rig.prefix+"-neighbor", "bridge", published)
	hostPort := neighbor.NetworkSettings.Ports[nat.Port(fenceNeighborPort+"/tcp")][0].HostPort
	gateway := neighbor.NetworkSettings.Networks["bridge"].Gateway
	// With egress, ADR 0039 leaves the host and its networks to the operator's host firewall.
	return []fenceprobe.Target{
		{Name: "other-project", Addr: net.JoinHostPort(tenant.NetworkSettings.Networks[other].IPAddress, fenceNeighborPort), Fenced: true},
		{Name: "default-bridge-neighbor", Addr: net.JoinHostPort(neighbor.NetworkSettings.Networks["bridge"].IPAddress, fenceNeighborPort), Fenced: !egress},
		{Name: "host", Addr: net.JoinHostPort(gateway, hostPort), Fenced: !egress},
		{Name: "metadata", Addr: "169.254.169.254:80", Fenced: !egress},
		{Name: "own-db", Addr: net.JoinHostPort(lab.rig.db, fenceDBPort)},
	}
}

func (lab *startFenceLab) startServer(t *testing.T, name, networkName string, published nat.PortMap) container.InspectResponse {
	t.Helper()
	ctx := context.Background()
	created, err := lab.rig.direct.ContainerCreate(ctx,
		&container.Config{Image: itBusybox, Cmd: []string{"httpd", "-f", "-p", fenceNeighborPort},
			ExposedPorts: nat.PortSet{nat.Port(fenceNeighborPort + "/tcp"): {}}},
		&container.HostConfig{NetworkMode: container.NetworkMode(networkName), PortBindings: published}, nil, nil, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lab.rig.direct.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
	if err := lab.rig.direct.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := lab.rig.direct.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return inspect
}

// control runs the probe on the default bridge and the other project's network, under no fence.
func (lab *startFenceLab) control(t *testing.T) []fenceprobe.Sample {
	t.Helper()
	ctx := context.Background()
	targets := []fenceprobe.Target{}
	for _, target := range lab.targets {
		if target.Name != "own-db" {
			targets = append(targets, target)
		}
	}
	created, err := lab.rig.direct.ContainerCreate(ctx, &container.Config{Image: itBusybox, Cmd: []string{"sh", "-c", fenceprobe.Script},
		Env: []string{fenceprobe.SecondsEnv + "=" + fenceSeconds, fenceprobe.TargetsEnv + "=" + fenceprobe.TargetsValue(targets)}},
		&container.HostConfig{NetworkMode: container.NetworkMode(lab.rig.prefix + "-other")}, nil, nil, lab.rig.prefix+"-control")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lab.rig.direct.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
	if err := lab.rig.direct.NetworkConnect(ctx, "bridge", created.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := lab.rig.direct.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	return lab.waitForSamples(t, func() (string, error) { return lab.containerLogs(ctx, created.ID) })
}

func (lab *startFenceLab) containerLogs(ctx context.Context, id string) (string, error) {
	stream, err := lab.rig.direct.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var out strings.Builder
	_, err = stdcopy.StdCopy(&out, &out, stream)
	return out.String(), err
}

// deployProbe deploys the probe as an app through the runtime, the way a deploy does.
func (lab *startFenceLab) deployProbe(t *testing.T) []fenceprobe.Sample {
	t.Helper()
	seconds, targets := fenceSeconds, fenceprobe.TargetsValue(lab.targets)
	app := &apphost.App{
		ID: "app-it", ProjectID: "proj-it", Name: "web", Image: itBusybox, Port: fenceprobe.Port, Replicas: 1, Tier: domain.Free,
		Args: []string{"sh", "-c", fenceprobe.Script}, Status: apphost.StatusCreated,
		Env: []apphost.EnvVar{
			{Name: fenceprobe.SecondsEnv, Kind: apphost.KindLiteral, Value: &seconds},
			{Name: fenceprobe.TargetsEnv, Kind: apphost.KindLiteral, Value: &targets},
		},
	}
	lab.rig.deploy(app, "dep-fence")
	ctx := context.Background()
	return lab.waitForSamples(t, func() (string, error) {
		page, err := lab.rig.rt.AppLogs(ctx, lab.rig.db, app.ID, k8s.AppLogOptions{TailLines: 1000})
		if err != nil {
			return "", err
		}
		var out strings.Builder
		for _, line := range page.Lines {
			out.WriteString(line.Text + "\n")
		}
		return out.String(), nil
	})
}

func (lab *startFenceLab) waitForSamples(t *testing.T, read func() (string, error)) []fenceprobe.Sample {
	t.Helper()
	var logs string
	waitFor(t, "the probe's last round", func() bool {
		var err error
		logs, err = read()
		return err == nil && strings.Contains(logs, fenceprobe.EndLine)
	})
	samples, err := fenceprobe.Parse(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe timeline:\n%s", fenceprobe.Timeline(samples))
	return samples
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 2m", what)
		}
		time.Sleep(time.Second)
	}
}

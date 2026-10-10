//go:build integration

package dockerapps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// Run: go test -tags=integration ./internal/dockerapps/ -run TestRealEngine -v
// The runtime drives the real engine through the real engine proxy, so every
// call it makes is one the proxy admits.

const itBusybox = "docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

type realRig struct {
	t                *testing.T
	direct           *client.Client
	rt               *Runtime
	db, edge, prefix string
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	direct, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("no engine: %v", err)
	}
	ctx := context.Background()
	if _, err := direct.Ping(ctx); err != nil {
		t.Skipf("no engine: %v", err)
	}
	prefix := "excalibase-it" + shortHash(t.Name()+time.Now().String(), 6)
	rig := &realRig{t: t, direct: direct, prefix: prefix, db: prefix + "-postgres", edge: prefix + "-edge"}
	pullDirect(t, direct, itBusybox)
	proxied := proxyTo(t, engineproxy.Policy{
		ManagedLabel: labelManaged, NetworkPrefix: prefix + "-net-", VolumePrefix: prefix + "-vol-",
		ContainerPrefix: "excalibase-", EdgeContainer: rig.edge,
	})
	// Registered after the proxy, so it runs while the proxy still serves.
	t.Cleanup(rig.cleanup)
	rig.run(rig.db, map[string]string{labelManaged: "true"})
	rig.run(rig.edge, nil)
	routes := t.TempDir()
	rig.rt, err = New(proxied, Options{
		NetworkPrefix: prefix + "-net-", VolumePrefix: prefix + "-vol-", EdgeContainer: rig.edge,
		EdgeTLSAddress: "127.0.0.1:1", RoutesDir: routes, ProbeBinary: buildProbe(t), ToolsImage: itBusybox,
		Route: apphost.Route{Domain: "apps.example.com", TLS: true}, Projects: fakeScopes{rig.db: "proj-it"},
		PollInterval: 200 * time.Millisecond, MinReady: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.rt.VerifyEngine(ctx); err != nil {
		t.Fatal(err)
	}
	return rig
}

func pullDirect(t *testing.T, engine *client.Client, ref string) {
	t.Helper()
	stream, err := engine.ImagePull(context.Background(), ref, image.PullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
}

// run starts a long-lived busybox the test owns, standing in for a database or the edge.
func (r *realRig) run(name string, labels map[string]string) {
	ctx := context.Background()
	created, err := r.direct.ContainerCreate(ctx, &container.Config{Image: itBusybox, Cmd: []string{"sleep", "3600"}, Labels: labels},
		&container.HostConfig{}, nil, nil, name)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.direct.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realRig) cleanup() {
	ctx := context.Background()
	if r.rt != nil {
		if err := r.rt.TeardownProject(ctx, "proj-it", r.db); err != nil {
			r.t.Errorf("teardown: %v", err)
		}
	}
	for _, name := range []string{r.db, r.edge} {
		_ = r.direct.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	}
}

func proxyTo(t *testing.T, policy engineproxy.Policy) *client.Client {
	t.Helper()
	socket := strings.TrimPrefix(client.DefaultDockerHost, "unix://")
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		socket = strings.TrimPrefix(host, "unix://")
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}
	handler := engineproxy.NewHandler(policy, &url.URL{Scheme: "http", Host: "engine"}, &http.Transport{DialContext: dial})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	proxied, err := client.NewClientWithOpts(client.WithHost("tcp://"+server.Listener.Addr().String()), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	return proxied
}

func buildProbe(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "probe")
	build := exec.Command("go", "build", "-o", out, "../../cmd/app-probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the probe: %v\n%s", err, output)
	}
	probe, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return probe
}

func itApp(body string) *apphost.App {
	return &apphost.App{
		ID: "app-it", ProjectID: "proj-it", Name: "web", Image: itBusybox, Port: 8080, Replicas: 1, Tier: domain.Free,
		Args: []string{"sh", "-c", "mkdir -p /www && echo " + body + " > /www/index.html && echo serving " + body +
			" && exec httpd -f -p 8080 -h /www"},
		HealthCheckPath: "/", Disk: &apphost.AppDisk{MountPath: "/data", Size: "1Gi"}, Status: apphost.StatusCreated,
	}
}

func (r *realRig) deploy(app *apphost.App, deployID string) {
	r.t.Helper()
	opts := RenderOptions("apps.example.com", "default")
	opts.DeployID, opts.EnvRevision = deployID, "1"
	workload, err := k8s.RenderAppWorkload(r.db, app, stubResolver{}, opts)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.rt.ApplyAppWorkload(ctx, r.db, workload); err != nil {
		r.t.Fatalf("apply %s: %v", deployID, err)
	}
	if err := r.rt.WaitForAppRollout(ctx, r.db, k8s.AppObjectName(app.Name), deployID, 2*time.Minute); err != nil {
		r.t.Fatalf("rollout %s: %v", deployID, err)
	}
}

// fromEdge fetches the app the way the edge does: by container name on the project network.
func (r *realRig) fromEdge(target string) (string, error) {
	output, err := exec.Command("docker", "exec", r.edge, "wget", "-qO-", "-T", "3", "http://"+target+":8080/").CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func (r *realRig) serving() string {
	r.t.Helper()
	record, err := r.rt.loadRecord("proj-it", "app-it")
	if err != nil || record == nil || len(record.Serving) != 1 {
		r.t.Fatalf("record %+v (%v)", record, err)
	}
	return record.Serving[0]
}

func TestRealEngineRunsAnAppThroughTheProxy(t *testing.T) {
	rig := newRealRig(t)
	ctx := context.Background()
	if err := rig.rt.CreateAppDisk(ctx, rig.db, itApp("one"), "", k8s.DiskJobOptions{Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	rig.deploy(itApp("one"), "dep-1")
	if body, err := rig.fromEdge(rig.serving()); err != nil || body != "one" {
		t.Fatalf("v1 from the edge: %q (%v)", body, err)
	}
	rig.deploy(itApp("two"), "dep-2")
	if body, err := rig.fromEdge(rig.serving()); err != nil || body != "two" {
		t.Fatalf("v2 from the edge: %q (%v)", body, err)
	}
	page, err := rig.rt.AppLogs(ctx, rig.db, "app-it", k8s.AppLogOptions{TailLines: 10})
	if err != nil || len(page.Lines) == 0 || !strings.Contains(page.Lines[0].Text, "serving two") {
		t.Fatalf("logs %+v (%v)", page.Lines, err)
	}
	if _, err := rig.rt.AppDiskUsage(ctx, rig.db, "app-it", *itApp("x").Disk, k8s.DiskJobOptions{Timeout: time.Minute}); err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	serving := rig.serving()
	if err := rig.rt.PauseAppWorkload(ctx, rig.db, "app-it"); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.fromEdge(serving); err == nil {
		t.Fatal("a paused app answered")
	}
	if err := rig.rt.ResumeAppWorkload(ctx, rig.db, "app-it", "web", domain.Free, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if body, err := rig.fromEdge(rig.serving()); err != nil || body != "two" {
		t.Fatalf("after resume: %q (%v)", body, err)
	}
	if err := rig.rt.DeleteAppWorkload(ctx, rig.db, "app-it", time.Minute); err != nil {
		t.Fatal(err)
	}
	list, err := rig.direct.ContainerList(ctx, container.ListOptions{All: true, Filters: labelFilter(labelApp, "app-it")})
	if err != nil || len(list) != 0 {
		t.Fatalf("containers left: %d (%v)", len(list), err)
	}
}

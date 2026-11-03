//go:build integration

package dockerbuilds

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// Run: go test -tags=integration ./internal/dockerbuilds/ -run TestRealEngine -v
// Against Docker 27 (the isolated gateway refused): scripts/dockerbuilds-docker27.sh.
// Real builds of a tiny static site through the real engine proxy: every call
// the runner makes is one the proxy admits. The sandbox runtime is the engine's
// default here (runsc is exercised by the single-host boot test in CI).

const itBuildWait = 6 * time.Minute

type buildRig struct {
	// engineMajor is the engine's major version: before 28 build networks keep a host address.
	engineMajor int
	t           *testing.T
	direct      *client.Client
	runner      *Runner
	prefix      string
	commit      string
	gitAddr     string
}

func newBuildRig(t *testing.T, configure func(*Options)) *buildRig {
	t.Helper()
	direct, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("no engine: %v", err)
	}
	ctx := context.Background()
	if _, err := direct.Ping(ctx); err != nil {
		t.Skipf("no engine: %v", err)
	}
	prefix := fmt.Sprintf("excalibase-it%d", time.Now().UnixNano()%1_000_000)
	rig := &buildRig{t: t, direct: direct, prefix: prefix}
	egressImage := buildEgressImage(t, direct, prefix)
	gateway := rig.egressGateway()
	rig.commit, rig.gitAddr = serveFixtureRepo(t, gateway)
	proxied := proxyTo(t, engineproxy.Policy{
		ManagedLabel: labelManaged, NetworkPrefix: prefix + "-net-", VolumePrefix: prefix + "-vol-", ContainerPrefix: "excalibase-",
	})
	t.Cleanup(rig.cleanup)
	options := Options{
		NetworkPrefix: prefix + "-net-", VolumePrefix: prefix + "-vol-", EgressImage: egressImage,
		PrepareImage: config.DefaultSiteBuildPrepareImage, EgressAllow: []string{rig.gitAddr}, DiskCheckEvery: 2 * time.Second,
	}
	if configure != nil {
		configure(&options)
	}
	rig.runner, err = NewRunner(proxied, options)
	if err != nil {
		t.Fatal(err)
	}
	// Verify is provisioning's startup check: it must pass on every engine, and say what it found.
	var startup bytes.Buffer
	log.SetOutput(io.MultiWriter(&startup, os.Stderr))
	err = rig.runner.Verify(ctx)
	log.SetOutput(os.Stderr)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if info, err := direct.ServerVersion(ctx); err == nil {
		rig.engineMajor = majorVersion(info.Version)
	}
	rig.assertStartupIsolation(startup.String())
	return rig
}

// assertStartupIsolation: Docker 28+ isolates build networks from the host; Docker 27 refuses the
// isolated gateway with a 400, and builds fall back to plain internal networks, saying so (EXC-671).
func (r *buildRig) assertStartupIsolation(startup string) {
	r.t.Helper()
	switch {
	case r.engineMajor >= 28 && !r.runner.HostIsolated():
		r.t.Fatalf("Docker %d isolates build networks from the host, but Verify did not see it", r.engineMajor)
	case r.engineMajor > 0 && r.engineMajor < 28:
		if r.runner.HostIsolated() || !strings.Contains(startup, "refused an isolated gateway") {
			r.t.Fatalf("Docker %d: isolated=%v, startup log %q", r.engineMajor, r.runner.HostIsolated(), startup)
		}
		if options := r.runner.buildNetworkOptions(nil).Options; len(options) != 0 {
			r.t.Fatalf("Docker %d: build networks still sent %v", r.engineMajor, options)
		}
	}
}

func majorVersion(version string) int {
	major, _, _ := strings.Cut(version, ".")
	number, _ := strconv.Atoi(major)
	return number
}

// egressGateway makes the shared egress network ahead of the runner, so the fixture's address on the host is known.
func (r *buildRig) egressGateway() string {
	r.t.Helper()
	ctx := context.Background()
	name := r.prefix + "-net-" + egressNetworkName
	if _, err := r.direct.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge",
		Labels: map[string]string{labelManaged: "true", labelComponent: componentBuild}}); err != nil {
		r.t.Fatalf("create the egress network: %v", err)
	}
	inspect, err := r.direct.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil || len(inspect.IPAM.Config) == 0 || inspect.IPAM.Config[0].Gateway == "" {
		r.t.Fatalf("egress network has no gateway: %v %+v", err, inspect.IPAM)
	}
	return inspect.IPAM.Config[0].Gateway
}

func (r *buildRig) cleanup() {
	ctx := context.Background()
	args := filters.NewArgs(filters.Arg("label", labelComponent+"="+componentBuild))
	containers, _ := r.direct.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.Contains(name, r.prefix) || strings.HasPrefix(strings.TrimPrefix(name, "/"), "excalibase-build-it-") {
				_ = r.direct.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
			}
		}
	}
	networks, _ := r.direct.NetworkList(ctx, network.ListOptions{Filters: args})
	for _, n := range networks {
		if strings.HasPrefix(n.Name, r.prefix) {
			_ = r.direct.NetworkRemove(ctx, n.ID)
		}
	}
	volumes, _ := r.direct.VolumeList(ctx, volume.ListOptions{Filters: args})
	for _, v := range volumes.Volumes {
		if strings.HasPrefix(v.Name, r.prefix) {
			_ = r.direct.VolumeRemove(ctx, v.Name, true)
		}
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
	server := httptest.NewServer(engineproxy.NewHandler(policy, &url.URL{Scheme: "http", Host: "engine"}, &http.Transport{DialContext: dial}))
	t.Cleanup(server.Close)
	proxied, err := client.NewClientWithOpts(client.WithHost("tcp://"+server.Listener.Addr().String()), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	return proxied
}

// buildEgressImage packs the egress proxy, built from this checkout, into a one-layer image.
func buildEgressImage(t *testing.T, direct *client.Client, prefix string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "excalibase-build-egress")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/build-egress")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the egress proxy: %v\n%s", err, output)
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var buildContext bytes.Buffer
	archive := tar.NewWriter(&buildContext)
	dockerfile := []byte("FROM scratch\nCOPY excalibase-build-egress /bin/excalibase-build-egress\n")
	for name, content := range map[string][]byte{"Dockerfile": dockerfile, "excalibase-build-egress": body} {
		_ = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))})
		_, _ = archive.Write(content)
	}
	_ = archive.Close()
	tag := prefix + "-egress:it"
	resp, err := direct.ImageBuild(context.Background(), &buildContext, types.ImageBuildOptions{Tags: []string{tag}, Remove: true})
	if err != nil {
		t.Fatalf("build the egress image: %v", err)
	}
	output, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(output), `"error"`) {
		t.Fatalf("build the egress image: %s", output)
	}
	t.Cleanup(func() { _, _ = direct.ImageRemove(context.Background(), tag, image.RemoveOptions{Force: true}) })
	return tag
}

// serveFixtureRepo serves a one-commit site over git's smart HTTP on every host
// address, and answers its commit and the host:port the egress sidecar reaches it at.
func serveFixtureRepo(t *testing.T, gateway string) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "site.git")
	work := filepath.Join(root, "work")
	files := map[string]string{
		"package.json": `{"name":"fixture","version":"1.0.0","private":true,"scripts":{"build":"node build.js"}}`,
		"build.js": `const fs = require("fs");
fs.mkdirSync("dist", { recursive: true });
fs.writeFileSync("dist/index.html", "<h1>" + process.env.GREETING + "</h1>");
`,
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Join(work, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=it", "GIT_AUTHOR_EMAIL=it@example.com",
			"GIT_COMMITTER_NAME=it", "GIT_COMMITTER_EMAIL=it@example.com")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(work, "init", "-q", "-b", "main")
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "fixture")
	git(root, "clone", "-q", "--bare", work, repo)
	git(repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	commit := git(work, "rev-parse", "HEAD")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: backend, ReadHeaderTimeout: 10 * time.Second}}
	server.Start()
	t.Cleanup(server.Close)
	port := listener.Addr().(*net.TCPAddr).Port
	return commit, fmt.Sprintf("%s:%d", gateway, port)
}

func (r *buildRig) spec(id string) sitebuilds.RunSpec {
	return sitebuilds.RunSpec{
		BuildID: "it-" + id + fmt.Sprint(time.Now().UnixNano()%100000), SiteID: "site-it", ProjectID: "proj-it",
		Image: sitebuilds.NodeImage("", "22"), CloneURL: "http://" + r.gitAddr + "/site.git", Commit: r.commit,
		CloneToken: "unused-by-the-fixture", OutputDir: "dist", CPUMillis: 1000, Memory: "1Gi", Disk: "2Gi",
		Timeout: 5 * time.Minute, Env: map[string]string{"GREETING": "hello from a single host"},
	}
}

func (r *buildRig) runToEnd(spec sitebuilds.RunSpec) sitebuilds.RunStatus {
	r.t.Helper()
	ctx := context.Background()
	if err := r.runner.Start(ctx, spec); err != nil {
		r.t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(itBuildWait)
	for time.Now().Before(deadline) {
		status, err := r.runner.Status(ctx, spec.BuildID)
		if err != nil {
			r.t.Fatalf("Status: %v", err)
		}
		if status.Phase == sitebuilds.RunReady || status.Phase == sitebuilds.RunFailed {
			return status
		}
		time.Sleep(time.Second)
	}
	r.t.Fatalf("build %s did not finish\n%s", spec.BuildID, r.logs(spec.BuildID))
	return sitebuilds.RunStatus{}
}

func (r *buildRig) logs(buildID string) string {
	logs, err := r.runner.Logs(context.Background(), buildID)
	if err != nil {
		return "logs: " + err.Error()
	}
	return logs
}

// collected unpacks the build's archive into name → content.
func (r *buildRig) collected(buildID string) map[string]string {
	r.t.Helper()
	var packed bytes.Buffer
	if err := r.runner.Collect(context.Background(), buildID, &packed, 10<<20); err != nil {
		r.t.Fatalf("Collect: %v", err)
	}
	unzipped, err := gzip.NewReader(&packed)
	if err != nil {
		r.t.Fatal(err)
	}
	files := map[string]string{}
	archive := tar.NewReader(unzipped)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			r.t.Fatal(err)
		}
		body, _ := io.ReadAll(archive)
		files[strings.TrimPrefix(header.Name, "./")] = string(body)
	}
}

func (r *buildRig) assertGone(buildID string) {
	r.t.Helper()
	ctx := context.Background()
	if err := r.runner.Remove(ctx, buildID); err != nil {
		r.t.Fatalf("Remove: %v", err)
	}
	args := filters.NewArgs(filters.Arg("label", labelBuild+"="+buildID))
	containers, _ := r.direct.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	volumes, _ := r.direct.VolumeList(ctx, volume.ListOptions{Filters: args})
	networks, _ := r.direct.NetworkList(ctx, network.ListOptions{Filters: args})
	if len(containers)+len(volumes.Volumes)+len(networks) != 0 {
		r.t.Fatalf("left behind: %d containers, %d volumes, %d networks", len(containers), len(volumes.Volumes), len(networks))
	}
}

// probeScript records what a build can reach: the internet only through the sidecar, nothing private.
const probeScript = `set -u
mkdir -p dist
reach() { if timeout 4 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; then echo "$3=open"; else echo "$3=closed"; fi; }
{
  reach 1.1.1.1 443 direct-internet
  reach 169.254.169.254 80 direct-metadata
  own=$(hostname -i | awk '{print $1}')
  reach "${own%.*}.1" "$FIXTURE_PORT" direct-host
  reach "$FIXTURE_HOST" "$FIXTURE_PORT" direct-fixture
  echo "proxy-metadata=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://169.254.169.254/latest/meta-data/)"
  echo "proxy-private=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://10.0.0.1/)"
  echo "proxy-smtp=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://example.com:25/)"
  echo "proxy-internet=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 https://registry.npmjs.org/)"
} > dist/probes.txt
npm run build`

func TestRealEngineBuildsAStaticSiteBehindTheEgressProxy(t *testing.T) {
	rig := newBuildRig(t, nil)
	spec := rig.spec("site")
	spec.BuildCommand = probeScript
	host, port, _ := net.SplitHostPort(rig.gitAddr)
	spec.Env["FIXTURE_HOST"], spec.Env["FIXTURE_PORT"] = host, port
	status := rig.runToEnd(spec)
	if status.Phase != sitebuilds.RunReady {
		t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
	}
	files := rig.collected(spec.BuildID)
	if files["index.html"] != "<h1>hello from a single host</h1>" {
		t.Fatalf("index.html %q\n%s", files["index.html"], rig.logs(spec.BuildID))
	}
	probes := files["probes.txt"]
	// Before Docker 28 the build network keeps its host address (the warned, documented gap).
	// The other bridge's address stays closed even there: Docker isolates bridges from each other.
	directHost := "direct-host=closed"
	if rig.engineMajor > 0 && rig.engineMajor < 28 {
		directHost = "direct-host=open"
	}
	for _, want := range []string{"direct-internet=closed", "direct-metadata=closed", directHost, "direct-fixture=closed",
		"proxy-metadata=403", "proxy-private=403", "proxy-smtp=403", "proxy-internet=200"} {
		if !strings.Contains(probes, want) {
			t.Errorf("probe %s missing:\n%s", want, probes)
		}
	}
	logs := rig.logs(spec.BuildID)
	if !strings.Contains(logs, "== network ==") || !strings.Contains(logs, "refused") || !strings.Contains(logs, "== clone ==") {
		t.Errorf("logs miss the steps:\n%s", logs)
	}
	if strings.Contains(logs, "unused-by-the-fixture") {
		t.Error("the clone token reached the logs")
	}
	rig.assertGone(spec.BuildID)
}

func TestRealEngineNamesLimitsAndFailures(t *testing.T) {
	rig := newBuildRig(t, nil)
	t.Run("install", func(t *testing.T) {
		spec := rig.spec("install")
		spec.InstallCommand = "echo broken; exit 1"
		if status := rig.runToEnd(spec); status.Failure != sitebuilds.FailInstall {
			t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
		}
		rig.assertGone(spec.BuildID)
	})
	t.Run("time", func(t *testing.T) {
		spec := rig.spec("time")
		spec.Timeout, spec.BuildCommand = 40*time.Second, "sleep 600"
		if status := rig.runToEnd(spec); status.Failure != sitebuilds.FailTimeout {
			t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
		}
		rig.assertGone(spec.BuildID)
	})
	t.Run("disk", func(t *testing.T) {
		spec := rig.spec("disk")
		spec.Disk, spec.BuildCommand = "64Mi", "head -c 200000000 /dev/zero > /work/fill && sleep 120"
		if status := rig.runToEnd(spec); status.Failure != sitebuilds.FailDisk {
			t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
		}
		rig.assertGone(spec.BuildID)
	})
	t.Run("unreadable folder", func(t *testing.T) {
		spec := rig.spec("hidden")
		spec.BuildCommand = "mkdir /work/hidden && chmod 000 /work/hidden && sleep 120"
		if status := rig.runToEnd(spec); status.Failure != sitebuilds.FailDisk || !strings.Contains(status.Detail, "measured") {
			t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
		}
		rig.assertGone(spec.BuildID)
	})
	t.Run("memory", func(t *testing.T) {
		spec := rig.spec("oom")
		spec.Memory, spec.InstallCommand = "256Mi", "true"
		spec.BuildCommand = `node -e "const held=[]; for(;;) held.push(Buffer.alloc(16<<20, 1))"`
		if status := rig.runToEnd(spec); status.Failure != sitebuilds.FailOutOfMemory {
			t.Fatalf("status %+v\n%s", status, rig.logs(spec.BuildID))
		}
		rig.assertGone(spec.BuildID)
	})
}

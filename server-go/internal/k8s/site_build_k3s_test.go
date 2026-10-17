//go:build live

package k8s

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

const (
	siteBuildTestImage = "node:22-bookworm"
	gitServerNamespace = "gitsrv"
	gitServerPort      = 9418
	siteBuildWait      = 12 * time.Minute
)

// TestK3sSiteBuild runs git builds of sites (EXC-354) through the real Job under
// gVisor: a Vite app built from an in-cluster git server and collected as the
// archive the upload path reads, an out-of-memory build named as such, and the
// egress fence holding a private address while the internet stays open.
func TestK3sSiteBuild(t *testing.T) {
	cluster := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := cluster.createNamespace(t, "excalibase-site-builds")
	commit := cluster.startGitServer(t)
	cluster.allowGitServer(t, namespace)
	runner := NewSiteBuildRunner(cluster.client, SiteBuildOptions{Namespace: namespace, RuntimeClass: gvisorRuntimeClass, Slots: 3,
		PrepareImage: config.DefaultSiteBuildPrepareImage})
	cloneURL := "git://gitsrv." + gitServerNamespace + ".svc.cluster.local/site.git"
	spec := func(buildID string) sitebuilds.RunSpec {
		return sitebuilds.RunSpec{
			BuildID: buildID, SiteID: "site-1", ProjectID: "proj-1", Image: sitebuilds.NodeImage("", "22"),
			CloneURL: cloneURL, Commit: commit, CloneToken: "unused-by-git-daemon", OutputDir: "dist",
			CPUMillis: 2000, Memory: "2Gi", Disk: "5Gi", Timeout: 10 * time.Minute,
			Env:         map[string]string{"VITE_GREETING": "hello from the fixture"},
			LargestPlan: sitebuilds.BuildSize{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi"},
		}
	}

	t.Run("builds the Vite app into an archive the upload path accepts", func(t *testing.T) {
		checkViteBuild(t, runner, spec("vite"), commit)
	})

	t.Run("an out-of-memory build is named, whatever its exit code", func(t *testing.T) {
		oom := spec("oom")
		oom.Memory, oom.InstallCommand = "512Mi", "true"
		oom.BuildCommand = `node -e "const held=[]; for(;;) held.push(Buffer.alloc(16<<20, 1))"`
		status := runToEnd(t, runner, oom)
		if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailOutOfMemory {
			t.Fatalf("status %+v\n%s", status, logsOf(runner, "oom"))
		}
		removeBuild(t, runner, "oom")
	})

	t.Run("the fence holds a private address and leaves the internet open", func(t *testing.T) {
		probe := spec("fence")
		probe.InstallCommand = "true"
		probe.BuildCommand = `set -e
if timeout 3 bash -c "exec 3<>/dev/tcp/${KUBERNETES_SERVICE_HOST}/443"; then echo "reached the kubernetes API"; exit 1; fi
if timeout 3 bash -c "exec 3<>/dev/tcp/169.254.169.254/80"; then echo "reached the metadata address"; exit 1; fi
timeout 20 bash -c "exec 3<>/dev/tcp/registry.npmjs.org/443"
mkdir -p dist && echo fenced > dist/index.html`
		status := runToEnd(t, runner, probe)
		if status.Phase != sitebuilds.RunReady {
			t.Fatalf("status %+v\n%s", status, logsOf(runner, "fence"))
		}
		removeBuild(t, runner, "fence")
	})
}

func checkViteBuild(t *testing.T, runner *SiteBuildRunner, spec sitebuilds.RunSpec, commit string) {
	t.Helper()
	status := runToEnd(t, runner, spec)
	if status.Phase != sitebuilds.RunReady {
		t.Fatalf("status %+v\n%s", status, logsOf(runner, spec.BuildID))
	}
	var archive bytes.Buffer
	if err := runner.Collect(context.Background(), spec.BuildID, &archive, 20<<20); err != nil {
		t.Fatalf("collect: %v", err)
	}
	bundle, err := sites.ReadArchive(&archive, sites.DefaultLimits, t.TempDir())
	if err != nil {
		t.Fatalf("the output is not a deployable archive: %v", err)
	}
	defer bundle.Close()
	if !bundleHas(t, bundle, ".js", "hello from the fixture") {
		t.Errorf("the declared build variable is not in the built script: %+v", bundle.Files)
	}
	logs := logsOf(runner, spec.BuildID)
	for _, want := range []string{"network fence up", "checked out " + commit, "npm ci", "vite build", "packed"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, spec.CloneToken) {
		t.Error("the clone token reached the build log")
	}
	removeBuild(t, runner, spec.BuildID)
}

func runToEnd(t *testing.T, runner *SiteBuildRunner, spec sitebuilds.RunSpec) sitebuilds.RunStatus {
	t.Helper()
	ctx := context.Background()
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatalf("start %s: %v", spec.BuildID, err)
	}
	deadline := time.Now().Add(siteBuildWait)
	for time.Now().Before(deadline) {
		status, err := runner.Status(ctx, spec.BuildID)
		if err != nil {
			t.Fatalf("status %s: %v", spec.BuildID, err)
		}
		if status.Phase == sitebuilds.RunReady || status.Phase == sitebuilds.RunFailed {
			return status
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("build %s did not finish in %s\n%s", spec.BuildID, siteBuildWait, logsOf(runner, spec.BuildID))
	return sitebuilds.RunStatus{}
}

func logsOf(runner *SiteBuildRunner, buildID string) string {
	logs, err := runner.Logs(context.Background(), buildID)
	if err != nil {
		return "logs: " + err.Error()
	}
	return logs
}

func removeBuild(t *testing.T, runner *SiteBuildRunner, buildID string) {
	t.Helper()
	if err := runner.Remove(context.Background(), buildID); err != nil {
		t.Errorf("remove %s: %v", buildID, err)
	}
}

func bundleHas(t *testing.T, bundle *sites.Bundle, suffix, content string) bool {
	t.Helper()
	for _, file := range bundle.Files {
		if !strings.HasSuffix(file.Path, suffix) {
			continue
		}
		reader, err := bundle.Open(file)
		if err != nil {
			t.Fatalf("open %s: %v", file.Path, err)
		}
		body, err := io.ReadAll(reader)
		_ = reader.Close()
		if err == nil && bytes.Contains(body, []byte(content)) {
			return true
		}
	}
	return false
}

// startGitServer serves the Vite fixture over git:// from a plain (runc) pod and answers its commit.
func (g *gvisorCluster) startGitServer(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	// Pulled once up front, so neither the git server nor a build spends its own deadline on it.
	for _, image := range []string{siteBuildTestImage, sitebuilds.NodeImage("", "22"), config.DefaultSiteBuildPrepareImage} {
		g.nodeShell(t, "crictl pull "+image)
	}
	g.createNamespace(t, gitServerNamespace)
	files := map[string]string{}
	root := filepath.Join("testdata", "site_build_vite")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		files[strings.ReplaceAll(relative, "/", "__")] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	fixture := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "fixture", Namespace: gitServerNamespace}, Data: files}
	if _, err := g.clientset.CoreV1().ConfigMaps(gitServerNamespace).Create(ctx, fixture, metav1.CreateOptions{}); err != nil {
		t.Fatalf("fixture configmap: %v", err)
	}
	script := `set -e
mkdir -p /app /srv/git
for f in /fixture/*; do target=/app/$(basename "$f" | sed 's#__#/#g'); mkdir -p "$(dirname "$target")"; cp "$f" "$target"; done
cd /app && git init -q -b main && git add -A
git -c user.name=fixture -c user.email=fixture@localhost commit -qm fixture
git clone -q --bare /app /srv/git/site.git && touch /srv/git/site.git/git-daemon-export-ok
git -C /srv/git/site.git rev-parse HEAD > /srv/head
exec git daemon --reuseaddr --export-all --base-path=/srv/git /srv/git`
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gitsrv", Namespace: gitServerNamespace, Labels: map[string]string{"app": "gitsrv"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "git", Image: siteBuildTestImage, Command: []string{"bash", "-c", script},
				Ports:          []corev1.ContainerPort{{ContainerPort: gitServerPort}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(gitServerPort)}}, PeriodSeconds: 2},
				VolumeMounts:   []corev1.VolumeMount{{Name: "fixture", MountPath: "/fixture"}},
			}},
			Volumes: []corev1.Volume{{Name: "fixture", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "fixture"}}}}},
		},
	}
	if _, err := g.clientset.CoreV1().Pods(gitServerNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("git server pod: %v", err)
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gitsrv", Namespace: gitServerNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "gitsrv"},
			Ports: []corev1.ServicePort{{Port: gitServerPort, TargetPort: intstr.FromInt(gitServerPort)}}},
	}
	if _, err := g.clientset.CoreV1().Services(gitServerNamespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatalf("git server service: %v", err)
	}
	g.waitPodReady(t, gitServerNamespace, "gitsrv")
	running, err := g.clientset.CoreV1().Pods(gitServerNamespace).Get(ctx, "gitsrv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("git server pod: %v", err)
	}
	return strings.TrimSpace(g.exec(t, *running, "cat /srv/head"))
}

// allowGitServer is the test's own NetworkPolicy: production clones from the public git host.
func (g *gvisorCluster) allowGitServer(t *testing.T, namespace string) {
	t.Helper()
	port := intstr.FromInt(gitServerPort)
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-git-server", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": gitServerNamespace}}}},
				Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
			}},
		},
	}
	if _, err := g.clientset.NetworkingV1().NetworkPolicies(namespace).Create(context.Background(), policy, metav1.CreateOptions{}); err != nil {
		t.Fatalf("test git policy: %v", err)
	}
}

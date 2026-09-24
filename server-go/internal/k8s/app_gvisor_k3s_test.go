//go:build live

package k8s

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Its master runs as root on :80, chowns its cache and drops workers to another user.
const rootImage = "nginx:1.27"

const (
	capChown  = 0
	capNetRaw = 13
)

const probeScript = "dmesg 2>&1; cat /proc/version"

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3sGVisor -v -timeout 30m
func TestK3sGVisor(t *testing.T) {
	for _, platform := range gvisorPlatforms {
		t.Run(platform, func(t *testing.T) { proveGVisor(t, platform) })
	}
}

func proveGVisor(t *testing.T, platform string) {
	g := startGVisorK3s(t, platform)
	namespace := g.createNamespace(t, "org1-proj1")
	rooted := liveApp("rooted", rootImage, 80)
	rooted.HealthCheckPath = "/"
	if err := g.deployApp(t, namespace, rooted, 4*time.Minute); err != nil {
		t.Fatalf("root image: want a rollout, got %v", err)
	}
	pod := g.appPod(t, namespace, "rooted")

	t.Run("root image serves port 80", func(t *testing.T) { assertServesAsRoot(t, g, pod) })
	t.Run("NET_RAW is not held", func(t *testing.T) { assertNoNetRaw(t, g, pod) })
	t.Run("runs under gVisor, a normal pod does not", func(t *testing.T) { assertGVisorSignature(t, g, pod) })
	t.Run("sandbox runs on the configured platform", func(t *testing.T) { assertSandboxPlatform(t, g, pod) })
	t.Run("root inside cannot see the host", func(t *testing.T) { assertHostInvisible(t, g, pod) })
	t.Run("no gVisor node fails fast", func(t *testing.T) { assertUnschedulableFailsFast(t, g, namespace) })
}

func assertServesAsRoot(t *testing.T, g *gvisorCluster, pod corev1.Pod) {
	if uid := strings.TrimSpace(g.exec(t, pod, "id -u")); uid != "0" {
		t.Fatalf("uid inside = %q, want 0", uid)
	}
	page := g.nodeShell(t, "wget -qO- http://"+pod.Status.PodIP+":80/")
	if !strings.Contains(page, "Welcome to nginx!") {
		t.Fatalf("GET :80 from the node = %q, want the nginx welcome page", page)
	}
}

func assertNoNetRaw(t *testing.T, g *gvisorCluster, pod corev1.Pod) {
	line := strings.TrimSpace(g.exec(t, pod, "grep CapEff /proc/1/status"))
	fields := strings.Fields(line)
	if len(fields) != 2 {
		t.Fatalf("unexpected CapEff line %q", line)
	}
	effective, err := strconv.ParseUint(fields[1], 16, 64)
	if err != nil {
		t.Fatalf("parse CapEff %q: %v", line, err)
	}
	t.Logf("pid 1 %s", line)
	if effective&(1<<capChown) == 0 {
		t.Errorf("CapEff %s lacks CHOWN; the granted set is not in effect", fields[1])
	}
	if effective&(1<<capNetRaw) != 0 {
		t.Errorf("CapEff %s holds NET_RAW", fields[1])
	}
}

func assertGVisorSignature(t *testing.T, g *gvisorCluster, pod corev1.Pod) {
	hostKernel := strings.TrimSpace(g.nodeShell(t, "uname -r"))
	sandboxed := g.exec(t, pod, probeScript)
	t.Logf("gVisor pod:\n%s", sandboxed)
	if !strings.Contains(sandboxed, "Starting gVisor") {
		t.Errorf("gVisor pod dmesg lacks the gVisor boot banner")
	}
	if strings.Contains(sandboxed, hostKernel) {
		t.Errorf("gVisor pod reports the host kernel %s", hostKernel)
	}

	control := g.exec(t, g.startControlPod(t, pod.Namespace), probeScript)
	t.Logf("runc control pod:\n%s", control)
	if strings.Contains(control, "Starting gVisor") {
		t.Errorf("a pod under the default runtime shows the gVisor banner")
	}
	if !strings.Contains(control, hostKernel) {
		t.Errorf("control pod should report the host kernel %s", hostKernel)
	}
}

// startControlPod runs the same image under the node's default runtime.
func (g *gvisorCluster) startControlPod(t *testing.T, namespace string) corev1.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "control", Namespace: namespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "control", Image: rootImage}}},
	}
	if _, err := g.clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create control pod: %v", err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		current, err := g.clientset.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err == nil && current.Status.Phase == corev1.PodRunning {
			return *current
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("control pod never ran")
	return corev1.Pod{}
}

// assertSandboxPlatform reads the sandbox process on the node: its flags name
// the platform, and only a KVM sandbox holds a KVM virtual machine open.
func assertSandboxPlatform(t *testing.T, g *gvisorCluster, pod corev1.Pod) {
	sandbox := g.nodeShell(t, `for p in /proc/[0-9]*; do
  c=$(tr '\0' ' ' < $p/cmdline 2>/dev/null)
  case "$c" in runsc-sandbox*`+string(pod.UID)+`*) echo "$c"; ls -l $p/fd 2>/dev/null;; esac
done`)
	if !strings.Contains(sandbox, "runsc-sandbox") {
		t.Fatalf("no runsc sandbox process for pod %s on the node", pod.UID)
	}
	if !strings.Contains(sandbox, "--platform="+g.platform) {
		t.Errorf("sandbox is not running --platform=%s", g.platform)
	}
	holdsVM := strings.Contains(sandbox, "anon_inode:kvm-vm")
	if holdsVM != (g.platform == gvisorPlatformKVM) {
		t.Errorf("sandbox holds a KVM VM = %v on platform %s", holdsVM, g.platform)
	}
	t.Logf("sandbox on %s: --platform=%s, holds kvm-vm fd: %v", g.platform, g.platform, holdsVM)
}

func assertHostInvisible(t *testing.T, g *gvisorCluster, pod corev1.Pod) {
	if init := g.exec(t, pod, "tr '\\0' ' ' < /proc/1/cmdline"); !strings.HasPrefix(init, "nginx: master process") {
		t.Errorf("pid 1 inside = %q, want the app's own process", init)
	}
	processes := g.exec(t, pod, "cat /proc/[0-9]*/cmdline | tr '\\0' ' '")
	for _, hostProcess := range []string{"k3s", "containerd", "runsc"} {
		if strings.Contains(processes, hostProcess) {
			t.Errorf("a host process (%s) is visible inside: %q", hostProcess, processes)
		}
	}
	nodeHostname := strings.TrimSpace(g.nodeShell(t, "cat /etc/hostname"))
	podHostname := strings.TrimSpace(g.exec(t, pod, "cat /etc/hostname"))
	if podHostname == nodeHostname || podHostname != pod.Name {
		t.Errorf("/etc/hostname inside = %q, node's = %q; want the pod's own %q", podHostname, nodeHostname, pod.Name)
	}
}

func assertUnschedulableFailsFast(t *testing.T, g *gvisorCluster, namespace string) {
	g.setGVisorLabel(t, false)
	t.Cleanup(func() { g.setGVisorLabel(t, true) })
	start := time.Now()
	err := g.deployApp(t, namespace, liveApp("stranded", rootImage, 80), 5*time.Minute)
	t.Logf("unschedulable after %s: %v", time.Since(start).Round(time.Second), err)
	if !errors.Is(err, ErrAppRollout) || !strings.Contains(err.Error(), "Unschedulable") ||
		!strings.Contains(err.Error(), `"`+gvisorRuntimeClass+`"`) {
		t.Fatalf("want an unschedulable failure naming the runtime class, got %v", err)
	}
	if time.Since(start) > 2*time.Minute {
		t.Errorf("took %s, want fail fast", time.Since(start))
	}
}

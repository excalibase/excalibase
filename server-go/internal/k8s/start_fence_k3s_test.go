//go:build live

package k8s

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fenceprobe"
)

// EXC-622: a pod's egress fence is programmed after the pod starts on some
// CNIs. These tests start the real workload with a probe as the first thing
// it runs and fail when a fenced address answers at any moment.

const (
	fenceProbeImage   = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	fenceProbeSeconds = "12"
	fenceNamespace    = "org1-fence"
	fenceControlNS    = "org1-fence-control"
	fenceTenantNS     = "org2-fence-tenant"
	fenceListenerPort = "80"
	fenceRuntimeRunc  = "runc"
	// A fenced deploy is repeated: a window may open on some starts only.
	fenceDeploysPerRuntime = 3
	// The current production chart's runtime image.
	fenceDenoImage = "excalibase/deno-runtime:1.5.1"
	fenceDenoPort  = "8000"
	// The production Cilium release (excalibase-service rke2/versions.env).
	productionCiliumChartVersion = "1.19.6"
)

// fakePrivateListeners stand in for the metadata service and the private
// networks a node can reach; they live on the node's loopback.
var fakePrivateListeners = map[string]string{
	"metadata-169.254.169.254": "169.254.169.254",
	"private-10.255.77.1":      "10.255.77.1",
	"private-172.16.77.1":      "172.16.77.1",
	"private-192.168.77.1":     "192.168.77.1",
}

// fenceLab is a cluster with the fixtures the probe dials.
type fenceLab struct {
	*egressLab
	targets []fenceprobe.Target
	// withoutFenceWait starts the function runtime without its wait-for-fence step.
	withoutFenceWait bool
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumStartFence -v -count=1 -timeout 40m
func TestK3sCiliumStartFence(t *testing.T) {
	lab := &egressLab{ctx: context.Background(), ciliumValues: productionCiliumValues, ciliumChart: productionCiliumChartVersion}
	lab.startCiliumClusterWith(t, testcontainers.WithCmdArgs("--disable-kube-proxy", "--disable=traefik", "--disable=servicelb"),
		testcontainers.WithFiles(gvisorFiles(t, gvisorPlatformSystrap)...))
	lab.createGVisorRuntimeClass(t)
	fence := newFenceLab(t, lab)
	fence.runAppStartFence(t, []string{gvisorRuntimeClass, fenceRuntimeRunc})
	fence.runFunctionStartFence(t, true)
	fence.runFunctionStartFence(t, false)
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sNetworkPolicyStartFence -v -count=1 -timeout 30m
// k3s's own policy controller (kube-router) enforces the function fence's NetworkPolicy, as on a default k3s AIO.
func TestK3sNetworkPolicyStartFence(t *testing.T) {
	lab := &egressLab{ctx: context.Background()}
	lab.startCluster(t)
	fence := newFenceLab(t, lab)
	fence.runFunctionStartFence(t, false)
}

// TestLiveClusterStartFence runs the same probes against a cluster that is
// already up, such as an RKE2 node: START_FENCE_KUBECONFIG names it and
// START_FENCE_RUNTIME_CLASS its sandbox RuntimeClass (gvisor by default).
func TestLiveClusterStartFence(t *testing.T) {
	path := os.Getenv("START_FENCE_KUBECONFIG")
	if path == "" {
		t.Skip("START_FENCE_KUBECONFIG is not set")
	}
	restCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	lab := &egressLab{ctx: context.Background()}
	lab.useCluster(t, restCfg)
	runtimeClass := envOrDefault("START_FENCE_RUNTIME_CLASS", gvisorRuntimeClass)
	fence := newFenceLab(t, lab)
	t.Cleanup(fence.cleanup)
	fence.runAppStartFence(t, []string{runtimeClass})
	fence.runFunctionStartFence(t, true)
	// What Cilium does on its own, which apps rely on: the runtime without its fence wait.
	fence.withoutFenceWait = true
	fence.runFunctionStartFence(t, true)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func newFenceLab(t *testing.T, lab *egressLab) *fenceLab {
	t.Helper()
	t.Setenv("POD_NAMESPACE", routePlatform)
	lab.createProjectNamespaces(t, map[string]string{fenceNamespace: "org1", fenceControlNS: "org1"})
	// Another tenant's plain pod; privileged so a CIS-hardened RKE2 admits it.
	tenant := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fenceTenantNS,
		Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}
	if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, tenant, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", fenceTenantNS, err)
	}
	lab.ensureRuncRuntimeClass(t)
	fence := &fenceLab{egressLab: lab}
	fence.startFakePrivateListeners(t)
	fence.targets = fence.collectTargets(t)
	return fence
}

// cleanup removes what a run on a cluster that outlives the test left behind.
func (lab *fenceLab) cleanup() {
	ctx := context.Background()
	for _, ns := range []string{fenceNamespace, fenceControlNS, fenceTenantNS} {
		_ = lab.cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	}
	_ = lab.cs.CoreV1().Pods("kube-system").Delete(ctx, "fence-fake-listeners", metav1.DeleteOptions{})
	_ = lab.cs.NodeV1().RuntimeClasses().Delete(ctx, "fence-runc", metav1.DeleteOptions{})
}

func (lab *egressLab) ensureRuncRuntimeClass(t *testing.T) {
	t.Helper()
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "fence-runc"}, Handler: fenceRuntimeRunc}
	if _, err := lab.cs.NodeV1().RuntimeClasses().Create(lab.ctx, runtimeClass, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create runc runtime class: %v", err)
	}
}

// startFakePrivateListeners serves HTTP on loopback addresses of the node in private ranges.
func (lab *fenceLab) startFakePrivateListeners(t *testing.T) {
	t.Helper()
	script := "mkdir -p /www && echo fake-private > /www/index.html"
	for _, ip := range fakePrivateListeners {
		script += fmt.Sprintf("; ip addr add %s/32 dev lo; httpd -p %s:%s -h /www", ip, ip, fenceListenerPort)
	}
	privileged := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fence-fake-listeners", Namespace: "kube-system"},
		Spec: corev1.PodSpec{
			HostNetwork: true,
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{{
				Name: "listen", Image: fenceProbeImage, Command: []string{"sh", "-c", script + "; sleep 86400"},
				SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
			}},
		},
	}
	if _, err := lab.cs.CoreV1().Pods("kube-system").Create(lab.ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create fake listeners: %v", err)
	}
	eventually(t, "fake listeners running", 3*time.Minute, func() bool {
		got, err := lab.cs.CoreV1().Pods("kube-system").Get(lab.ctx, pod.Name, metav1.GetOptions{})
		return err == nil && got.Status.Phase == corev1.PodRunning
	})
}

func (lab *fenceLab) collectTargets(t *testing.T) []fenceprobe.Target {
	t.Helper()
	runPod(lab.ctx, t, lab.cs, fenceTenantNS, "target", map[string]string{"role": "target"}, []string{"/agnhost", "netexec", "--http-port=8080"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: fenceTenantNS},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"role": "target"}, Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	created, err := lab.cs.CoreV1().Services(fenceTenantNS).Create(lab.ctx, svc, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create tenant service: %v", err)
	}
	kubeAPI, err := lab.cs.CoreV1().Services("default").Get(lab.ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get kubernetes service: %v", err)
	}
	dns, err := lab.cs.CoreV1().Services("kube-system").List(lab.ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})
	if err != nil || len(dns.Items) != 1 {
		t.Fatalf("want one kube-dns service, got %v: %v", dns, err)
	}
	nodeIP := nodeInternalIP(lab.ctx, t, lab.cs)
	targets := []fenceprobe.Target{
		{Name: "tenant-pod", Addr: net.JoinHostPort(podIP(lab.ctx, t, lab.cs, fenceTenantNS, "target"), "8080"), Fenced: true},
		{Name: "tenant-svc", Addr: net.JoinHostPort(created.Spec.ClusterIP, "8080"), Fenced: true},
		{Name: "kube-api-svc", Addr: net.JoinHostPort(kubeAPI.Spec.ClusterIP, "443"), Fenced: true},
		{Name: "node-kubelet", Addr: net.JoinHostPort(nodeIP, kubeletPort), Fenced: true},
		{Name: "dns", Addr: net.JoinHostPort(dns.Items[0].Spec.ClusterIP, "53")},
	}
	for name, ip := range fakePrivateListeners {
		targets = append(targets, fenceprobe.Target{Name: name, Addr: net.JoinHostPort(ip, fenceListenerPort), Fenced: true})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets
}

func (lab *fenceLab) probeEnv(extra ...fenceprobe.Target) string {
	return fenceprobe.TargetsValue(append(append([]fenceprobe.Target{}, lab.targets...), extra...))
}

func fenceProbeApp(id, name, targets string) *apphost.App {
	seconds, value := fenceProbeSeconds, targets
	return &apphost.App{ID: id, ProjectID: "proj-fence", Name: name, Image: fenceProbeImage, Port: fenceprobe.Port,
		Args: []string{"sh", "-c", fenceprobe.Script}, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated,
		Env: []apphost.EnvVar{
			{Name: fenceprobe.SecondsEnv, Kind: apphost.KindLiteral, Value: &seconds},
			{Name: fenceprobe.TargetsEnv, Kind: apphost.KindLiteral, Value: &value},
		}}
}

// runAppStartFence deploys the probe as an app the production way and checks every sample.
func (lab *fenceLab) runAppStartFence(t *testing.T, runtimeClasses []string) {
	t.Helper()
	for _, runtimeClass := range runtimeClasses {
		className := runtimeClass
		if className == fenceRuntimeRunc {
			className = "fence-runc"
		}
		t.Run("app control without a fence/"+runtimeClass, func(t *testing.T) {
			samples := lab.deployProbeApp(t, fenceControlNS, "ctl-"+runtimeClass, className, false)
			lab.requireReachable(t, samples)
		})
		for attempt := 1; attempt <= fenceDeploysPerRuntime; attempt++ {
			t.Run(fmt.Sprintf("app under its fence/%s/%d", runtimeClass, attempt), func(t *testing.T) {
				samples := lab.deployProbeApp(t, fenceNamespace, fmt.Sprintf("p%d-%s", attempt, runtimeClass), className, true)
				lab.requireFenced(t, samples, 0)
			})
		}
	}
}

func (lab *fenceLab) deployProbeApp(t *testing.T, namespace, name, runtimeClass string, fenced bool) []fenceprobe.Sample {
	t.Helper()
	name = strings.ReplaceAll(name, "-", "")
	app := fenceProbeApp("app-"+name+"-"+namespace, name, lab.probeEnv())
	options := AppRenderOptions{RuntimeClass: runtimeClass, Route: liveRoute, DeployID: liveDeployID, EnvRevision: "1"}
	workload, err := RenderAppWorkload(namespace, app, &fakeResolver{namespace: namespace}, options)
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	if fenced {
		err = lab.client.ApplyAppWorkload(lab.ctx, namespace, workload)
	} else {
		_, err = lab.client.applyAppDeployment(lab.ctx, namespace, workload.Deployment)
	}
	if err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
	pod := lab.waitForProbe(t, namespace, "excalibase.io/app="+app.ID, app.Name)
	return lab.readSamples(t, namespace, pod, app.Name)
}

// waitForProbe waits until the probe container has logged its last round.
func (lab *fenceLab) waitForProbe(t *testing.T, namespace, selector, container string) string {
	t.Helper()
	var pod string
	eventually(t, "probe finished in "+selector, 5*time.Minute, func() bool {
		pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil || len(pods.Items) == 0 {
			return false
		}
		pod = pods.Items[0].Name
		logs, err := lab.cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container}).DoRaw(lab.ctx)
		return err == nil && strings.Contains(string(logs), fenceprobe.EndLine)
	})
	return pod
}

func (lab *fenceLab) readSamples(t *testing.T, namespace, pod, container string) []fenceprobe.Sample {
	t.Helper()
	logs, err := lab.cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container}).DoRaw(lab.ctx)
	if err != nil {
		t.Fatalf("read probe logs of %s: %v", pod, err)
	}
	lab.logPodTimes(t, namespace, pod)
	samples, err := fenceprobe.Parse(string(logs))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe timeline:\n%s", fenceprobe.Timeline(samples))
	return samples
}

// logPodTimes records when the pod was created and its containers started, beside the probe's own clock.
func (lab *fenceLab) logPodTimes(t *testing.T, namespace, pod string) {
	t.Helper()
	got, err := lab.cs.CoreV1().Pods(namespace).Get(lab.ctx, pod, metav1.GetOptions{})
	if err != nil {
		return
	}
	line := "pod " + pod + " created " + got.CreationTimestamp.UTC().Format(time.RFC3339)
	for _, status := range append(append([]corev1.ContainerStatus{}, got.Status.InitContainerStatuses...), got.Status.ContainerStatuses...) {
		if status.State.Running != nil {
			line += fmt.Sprintf("; %s started %s", status.Name, status.State.Running.StartedAt.UTC().Format(time.RFC3339))
		}
		if status.State.Terminated != nil {
			line += fmt.Sprintf("; %s ran %s..%s", status.Name, status.State.Terminated.StartedAt.UTC().Format(time.RFC3339),
				status.State.Terminated.FinishedAt.UTC().Format(time.RFC3339))
		}
	}
	t.Log(line)
}

// requireReachable proves the probe reaches every target without a fence, so a refusal under one means something.
func (lab *fenceLab) requireReachable(t *testing.T, samples []fenceprobe.Sample) {
	t.Helper()
	for _, target := range lab.targets {
		if fenceprobe.FirstOpen(samples, target.Name, 0) < 0 {
			t.Errorf("without a fence %s (%s) never answered; a refusal under the fence would prove nothing", target.Name, target.Addr)
		}
	}
}

// requireFenced fails on any fenced target answering at or after from, and needs DNS to have answered.
func (lab *fenceLab) requireFenced(t *testing.T, samples []fenceprobe.Sample, from float64) {
	t.Helper()
	for _, breach := range fenceprobe.Breaches(samples, lab.targets, from) {
		t.Errorf("after t=%.2fs: %s", from, breach)
	}
	if fenceprobe.FirstOpen(samples, "dns", from) < 0 {
		t.Errorf("DNS never answered, so the network was never up and the refusals prove nothing")
	}
}

// runFunctionStartFence starts the function runtime the way EnsureDenoRuntime
// does, with the probe beside it in the pod, and checks every sample from the
// moment the pod's containers start. Without a fence the control proves the
// probe reaches everything; its runtime then waits for a fence that never
// comes, so the control runs the runtime without that wait.
func (lab *fenceLab) runFunctionStartFence(t *testing.T, ciliumFQDN bool) {
	t.Helper()
	mode := "networkpolicy"
	if ciliumFQDN {
		mode = "cilium"
	}
	if lab.withoutFenceWait {
		mode += " without the fence wait"
	}
	spec := DenoRuntimeSpec{Image: fenceDenoImage, RuntimeSecret: "fence-secret", CiliumFQDN: ciliumFQDN}
	t.Run("function runtime control without a fence/"+mode, func(t *testing.T) {
		lab.requireReachable(t, lab.startFunctionProbe(t, fenceControlNS, spec, false))
	})
	for attempt := 1; attempt <= fenceDeploysPerRuntime; attempt++ {
		t.Run(fmt.Sprintf("function runtime under its fence/%s/%d", mode, attempt), func(t *testing.T) {
			samples := lab.startFunctionProbe(t, fenceNamespace, spec, true)
			if fenceprobe.FirstOpen(samples, fenceRuntimeTarget.Name, 0) < 0 {
				t.Fatalf("the runtime never listened during the probe")
			}
			lab.requireFenced(t, samples, 0)
		})
	}
}

var fenceRuntimeTarget = fenceprobe.Target{Name: "runtime", Addr: net.JoinHostPort("127.0.0.1", fenceDenoPort)}

func (lab *fenceLab) startFunctionProbe(t *testing.T, namespace string, spec DenoRuntimeSpec, fenced bool) []fenceprobe.Sample {
	t.Helper()
	deployment := buildDenoDeployment(namespace, spec)
	if !fenced || lab.withoutFenceWait {
		deployment.Spec.Template.Spec.InitContainers = nil
	}
	deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, corev1.Container{
		Name: "probe", Image: fenceProbeImage, Command: []string{"sh", "-c", fenceprobe.Script},
		Env: []corev1.EnvVar{{Name: fenceprobe.SecondsEnv, Value: fenceProbeSeconds}, {Name: fenceprobe.TargetsEnv, Value: lab.probeEnv(fenceRuntimeTarget)}},
	})
	lab.removeFunctionRuntime(t, namespace)
	// The order EnsureDenoRuntime creates a new runtime in.
	if fenced {
		if err := lab.client.applyDenoEgressPolicy(lab.ctx, namespace, spec); err != nil {
			t.Fatalf("apply runtime fence: %v", err)
		}
	}
	if _, err := lab.cs.AppsV1().Deployments(namespace).Create(lab.ctx, deployment, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	_, err := lab.cs.CoreV1().Services(namespace).Create(lab.ctx, buildDenoService(namespace, deployment.Labels), metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create runtime service: %v", err)
	}
	pod := lab.waitForProbe(t, namespace, "app="+denoRuntimeName, "probe")
	return lab.readSamples(t, namespace, pod, "probe")
}

// removeFunctionRuntime deletes the runtime and its fences, so each start is a fresh pod under a fresh fence.
func (lab *fenceLab) removeFunctionRuntime(t *testing.T, namespace string) {
	t.Helper()
	now := int64(0)
	_ = lab.cs.AppsV1().Deployments(namespace).Delete(lab.ctx, denoRuntimeName, metav1.DeleteOptions{GracePeriodSeconds: &now})
	pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "app=" + denoRuntimeName})
	if err == nil {
		for _, pod := range pods.Items {
			_ = lab.cs.CoreV1().Pods(namespace).Delete(lab.ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &now})
		}
	}
	_ = lab.cs.NetworkingV1().NetworkPolicies(namespace).Delete(lab.ctx, denoEgressPolicyName, metav1.DeleteOptions{})
	_ = lab.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace).Delete(lab.ctx, denoEgressPolicyName, metav1.DeleteOptions{})
	eventually(t, "previous runtime gone", 2*time.Minute, func() bool {
		pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "app=" + denoRuntimeName})
		return err == nil && len(pods.Items) == 0
	})
}

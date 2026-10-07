//go:build live

package k8s

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/testcontainers/testcontainers-go"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	edgeClientApp      = "client-app"
	edgeClientFunction = "client-function"
	edgeStatsPort      = 1024
	edgePlatformDB     = "platform-db"
)

// liveEdge is the HAProxy edge the way rke2/install-platform.sh installs it: listeners 8080/8443 behind hostPort 80/443.
var liveEdge = EdgePeer{
	Namespace: haproxyNamespace,
	Labels:    map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"},
	Ports:     []int{8080, 8443},
}

// The production datapath: Cilium replaces kube-proxy and serves hostPort, with socket LB in the host namespace only.
var productionCiliumValues = map[string]interface{}{
	"kubeProxyReplacement": true,
	"socketLB":             map[string]interface{}{"hostNamespaceOnly": true},
}

// A public platform host resolves to a node; its hostPort lands on the edge pod, which the fences must admit (EXC-558).
// The same fences let an app and a function reach their own database by its service name, and nothing else on 5432,
// and the function's Cilium fence admits its outbound allowlist by name only.
//
// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumWorkloadsReachTheEdge -v -count=1 -timeout 40m
func TestK3sCiliumWorkloadsReachTheEdge(t *testing.T) {
	lab := &egressLab{ctx: context.Background(), ciliumValues: productionCiliumValues}
	lab.startCiliumClusterWith(t,
		testcontainers.WithCmdArgs("--disable-kube-proxy", "--disable=traefik", "--disable=servicelb"),
		testcontainers.WithFiles(gvisorFiles(t, gvisorPlatformSystrap)...))
	lab.nodeIP = nodeInternalIP(lab.ctx, t, lab.cs)
	lab.createGVisorRuntimeClass(t)
	t.Setenv("POD_NAMESPACE", routePlatform)
	lab.createNamespaces(t, haproxyNamespace, routePlatform, egressNamespaceB)
	lab.createProjectNamespaces(t, map[string]string{egressNamespaceA: "org1"})
	lab.installHAProxyEdge(t)
	lab.startTenantB(t)
	platformDBLabels := map[string]string{"cnpg.io/cluster": edgePlatformDB, "cnpg.io/podRole": "instance"}
	runPod(lab.ctx, t, lab.cs, routePlatform, edgePlatformDB, platformDBLabels, []string{"/agnhost", "netexec", "--http-port=5432"})
	kubeAPISvc, err := lab.cs.CoreV1().Services("default").Get(lab.ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get kubernetes service: %v", err)
	}
	lab.kubeAPIAddr = kubeAPISvc.Spec.ClusterIP + ":443"
	ownDB, otherDatabases := lab.startDatabases(t)

	app := egressApp("app-edge", "webedge")
	app.Env = []apphost.EnvVar{databaseURLReference()}
	lab.clients = map[string]egressClient{edgeClientApp: {app: app}}
	runSandboxedPod(lab.ctx, t, lab, edgeClientApp, appSelectorLabels(app))
	runPod(lab.ctx, t, lab.cs, egressNamespaceA, edgeClientFunction, map[string]string{"app": denoRuntimeName}, []string{"/bin/sleep", "3600"})

	edgeHTTPS := net.JoinHostPort(lab.nodeIP, "443")
	edgeHTTP := net.JoinHostPort(lab.nodeIP, "80")
	refused := append(lab.fencedTargets(t), otherDatabases...)
	clients := []string{edgeClientApp, edgeClientFunction}
	lab.expectReachableBeforeFences(t, clients, append([]string{edgeHTTPS, edgeHTTP, ownDB}, refused...))
	hasInternet := lab.connect(edgeClientFunction, "one.one.one.one:443") == nil

	t.Run("without the edge rule the public host is refused", func(t *testing.T) {
		lab.applyEdgeFences(t, app, EdgePeer{})
		for _, pod := range clients {
			eventually(t, pod+" refused at the edge", time.Minute, func() bool { return lab.connect(pod, edgeHTTPS) != nil })
		}
	})
	lab.applyEdgeFences(t, app, liveEdge)
	for _, pod := range clients {
		lab.expectFenced(t, pod, pod, []string{edgeHTTPS, edgeHTTP, ownDB}, refused)
	}

	allowlist := []string{"one.one.one.one:443"}
	t.Run("the function NetworkPolicy fence opens any public host on an allowlisted port", func(t *testing.T) {
		if !hasInternet {
			t.Skip("no outbound internet in this environment")
		}
		lab.applyFunctionFence(t, DenoRuntimeSpec{Edge: liveEdge, AllowedHosts: allowlist})
		eventually(t, "a host off the allowlist answers", time.Minute, func() bool { return lab.connect(edgeClientFunction, "dns.google:443") == nil })
	})
	lab.applyFunctionFence(t, DenoRuntimeSpec{Edge: liveEdge, AllowedHosts: allowlist, CiliumFQDN: true})
	t.Run("the function Cilium fence admits the allowlist by name and port only", func(t *testing.T) {
		if !hasInternet {
			t.Skip("no outbound internet in this environment")
		}
		eventually(t, "the allowlisted host answers", time.Minute, func() bool { return lab.connect(edgeClientFunction, "one.one.one.one:443") == nil })
		refusedFor(t, "the allowlisted host on another port", func() error { return lab.connect(edgeClientFunction, "one.one.one.one:80") })
		refusedFor(t, "a host off the allowlist", func() error { return lab.connect(edgeClientFunction, "dns.google:443") })
	})
	if _, err := lab.cs.NetworkingV1().NetworkPolicies(egressNamespaceA).Get(lab.ctx, denoEgressPolicyName, metav1.GetOptions{}); err == nil {
		t.Error("the function NetworkPolicy fence must be gone once the Cilium fence is in place")
	}
	lab.expectFenced(t, edgeClientFunction+" under the Cilium fence", edgeClientFunction, []string{edgeHTTPS, edgeHTTP, ownDB}, refused)
}

// expectReachableBeforeFences: every address answers each client before any fence, or a refusal after proves nothing.
func (lab *egressLab) expectReachableBeforeFences(t *testing.T, pods, addrs []string) {
	t.Helper()
	for _, pod := range pods {
		for _, addr := range addrs {
			eventually(t, "before any fence: "+pod+" reaches "+addr, 2*time.Minute, func() bool { return lab.connect(pod, addr) == nil })
		}
	}
}

// expectFenced: every reachable address answers, every refused one stays closed, and metadata never answers.
func (lab *egressLab) expectFenced(t *testing.T, client, pod string, reachable, refused []string) {
	t.Helper()
	t.Run(client+" reaches the edge and its own database", func(t *testing.T) {
		for _, addr := range reachable {
			eventually(t, addr, time.Minute, func() bool { return lab.connect(pod, addr) == nil })
		}
	})
	t.Run(client+" stays fenced from everything else", func(t *testing.T) {
		for _, addr := range refused {
			eventually(t, addr+" refused", time.Minute, func() bool { return lab.connect(pod, addr) != nil })
		}
		if lab.connect(pod, "169.254.169.254:80") == nil {
			t.Error("cloud metadata address answered")
		}
	})
}

// startDatabases runs the project's database behind its -rw service, a pod in the project that is not its database,
// and another tenant's database, all on 5432. It returns the own database's service address and the other two.
func (lab *egressLab) startDatabases(t *testing.T) (string, []string) {
	t.Helper()
	netexec := []string{"/agnhost", "netexec", "--http-port=5432"}
	runPod(lab.ctx, t, lab.cs, egressNamespaceA, "db", databasePodLabels(), netexec)
	runPod(lab.ctx, t, lab.cs, egressNamespaceA, egressNotADatabase, nil, netexec)
	runPod(lab.ctx, t, lab.cs, egressNamespaceB, "db", databasePodLabels(), netexec)
	service := "proja-postgres-rw"
	_, err := lab.cs.CoreV1().Services(egressNamespaceA).Create(lab.ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: service, Namespace: egressNamespaceA},
		Spec: corev1.ServiceSpec{
			Selector: databasePodLabels(),
			Ports:    []corev1.ServicePort{{Port: postgresPortNumber}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create database service: %v", err)
	}
	postgresPort := strconv.Itoa(postgresPortNumber)
	return net.JoinHostPort(service+"."+egressNamespaceA+".svc.cluster.local", postgresPort), []string{
		net.JoinHostPort(podIP(lab.ctx, t, lab.cs, egressNamespaceA, egressNotADatabase), postgresPort),
		net.JoinHostPort(podIP(lab.ctx, t, lab.cs, egressNamespaceB, "db"), postgresPort),
	}
}

// databaseURLReference is how an app is given its database: a reference resolved to the in-cluster address.
func databaseURLReference() apphost.EnvVar {
	return apphost.EnvVar{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
		SourceKind: apphost.SourceDatabase, SourceName: "proja", Variable: "DATABASE_URL",
	}}
}

// fencedTargets are reachable before any fence and must be refused after: the edge's non-listener port, another
// tenant, the platform database, the Kubernetes API and the node.
func (lab *egressLab) fencedTargets(t *testing.T) []string {
	t.Helper()
	edgePods, err := lab.cs.CoreV1().Pods(haproxyNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kubernetes-ingress"})
	if err != nil || len(edgePods.Items) != 1 {
		t.Fatalf("want one edge pod, got %v: %v", edgePods, err)
	}
	return []string{
		net.JoinHostPort(edgePods.Items[0].Status.PodIP, strconv.Itoa(edgeStatsPort)),
		lab.targetPodIP + ":8080",
		lab.targetSvcIP + ":8080",
		net.JoinHostPort(podIP(lab.ctx, t, lab.cs, routePlatform, edgePlatformDB), "5432"),
		lab.kubeAPIAddr,
		net.JoinHostPort(lab.nodeIP, kubeAPIServerPort),
		net.JoinHostPort(lab.nodeIP, kubeletPort),
	}
}

// applyEdgeFences applies both fences through the code paths a deploy uses.
func (lab *egressLab) applyEdgeFences(t *testing.T, app *apphost.App, edge EdgePeer) {
	t.Helper()
	options := AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Edge: edge, Route: liveRoute, EnvRevision: "1", DeployID: liveDeployID}
	workload, err := RenderAppWorkload(egressNamespaceA, app, &fakeResolver{namespace: egressNamespaceA}, options)
	if err != nil {
		t.Fatalf("render app: %v", err)
	}
	if err := lab.client.applyAppPolicy(lab.ctx, egressNamespaceA, workload.EgressPolicy, "egress"); err != nil {
		t.Fatalf("apply app fence: %v", err)
	}
	lab.applyFunctionFence(t, DenoRuntimeSpec{Edge: edge})
}

func (lab *egressLab) applyFunctionFence(t *testing.T, spec DenoRuntimeSpec) {
	t.Helper()
	if err := lab.client.applyDenoEgressPolicy(lab.ctx, egressNamespaceA, spec); err != nil {
		t.Fatalf("apply function fence: %v", err)
	}
}

// installHAProxyEdge installs the edge as rke2/install-platform.sh does: a DaemonSet on hostPort 80/443, no Service.
func (lab *egressLab) installHAProxyEdge(t *testing.T) {
	t.Helper()
	locate := action.ChartPathOptions{RepoURL: haproxyChartRepo, Version: haproxyChartVersion}
	chartPath, err := locate.LocateChart("kubernetes-ingress", cli.New())
	if err != nil {
		t.Fatalf("fetch haproxy chart %s: %v", haproxyChartVersion, err)
	}
	values := map[string]interface{}{"controller": map[string]interface{}{
		"kind":                 "DaemonSet",
		"daemonset":            map[string]interface{}{"useHostPort": true, "hostPorts": map[string]interface{}{"stat": nil}},
		"service":              map[string]interface{}{"enabled": false},
		"ingressClass":         liveRoute.IngressClass,
		"ingressClassResource": map[string]interface{}{"name": liveRoute.IngressClass},
	}}
	if err := lab.client.InstallHelmChart(lab.ctx, haproxyNamespace, "haproxy", chartPath, values); err != nil {
		t.Fatalf("install haproxy edge: %v", err)
	}
	eventually(t, "edge pod ready", 5*time.Minute, func() bool {
		pods, err := lab.cs.CoreV1().Pods(haproxyNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kubernetes-ingress"})
		if err != nil || len(pods.Items) != 1 {
			return false
		}
		for _, condition := range pods.Items[0].Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

// runSandboxedPod runs an app client under gVisor, as every app pod runs in production.
func runSandboxedPod(ctx context.Context, t *testing.T, lab *egressLab, name string, labels map[string]string) {
	t.Helper()
	runtimeClass := gvisorRuntimeClass
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: egressNamespaceA, Labels: labels},
		Spec: corev1.PodSpec{
			RuntimeClassName: &runtimeClass,
			Containers:       []corev1.Container{{Name: "main", Image: agnhostImage, Command: []string{"/bin/sleep", "3600"}}},
			RestartPolicy:    corev1.RestartPolicyNever,
		},
	}
	if _, err := lab.cs.CoreV1().Pods(egressNamespaceA).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
	eventually(t, "sandboxed pod "+name+" running", 3*time.Minute, func() bool {
		got, err := lab.cs.CoreV1().Pods(egressNamespaceA).Get(ctx, name, metav1.GetOptions{})
		return err == nil && got.Status.Phase == corev1.PodRunning
	})
}

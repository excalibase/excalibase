//go:build integration

package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const agnhostImage = "registry.k8s.io/e2e-test-images/agnhost:2.47"

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3sAppEgress -v -count=1 -timeout 15m
func TestK3sAppEgress(t *testing.T) {
	lab := newEgressLab(t)
	checks := lab.checks()

	// Before any policy, every refusal below must be reachable, or a refusal
	// after it proves nothing.
	unprovable := map[string]bool{}
	for _, check := range checks {
		if check.control {
			eventually(t, "before policy: "+check.name, 60*time.Second, func() bool { return lab.connect(check.pod, check.addr) == nil })
		}
		if check.controlIfReachable && lab.connect(check.pod, check.addr) != nil {
			unprovable[check.name] = true
		}
	}
	hasInternet := lab.connect(egressClientWithDB, "1.1.1.1:443") == nil

	lab.applyPolicies(t)
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if check.needsInternet && !hasInternet {
				t.Skip("no outbound internet in this environment")
			}
			if unprovable[check.name] {
				t.Skip("unreachable before the policy too, so a refusal would prove nothing")
			}
			eventually(t, check.name, 60*time.Second, func() bool {
				return (lab.connect(check.pod, check.addr) == nil) == check.reachable
			})
		})
	}
	t.Run("DNS resolution works", func(t *testing.T) {
		eventually(t, "DNS resolves", 30*time.Second, func() bool {
			err := lab.connect(egressClientWithDB, "kubernetes.default.svc.cluster.local:443")
			return err == nil || !strings.Contains(err.Error(), "DNS:")
		})
	})
}

const (
	egressNamespaceA      = "org1-proja"
	egressNamespaceB      = "org2-projb"
	egressClientWithDB    = "client-with-db"
	egressClientNoDB      = "client-no-db"
	egressClientExtraDeny = "client-extra-deny"
	egressExtraDeniedCIDR = "1.1.1.1/32"
	egressPublicAddress   = "1.1.1.1:443"
)

type egressCheck struct {
	name          string
	pod, addr     string
	reachable     bool
	control       bool
	needsInternet bool
	// Checked before the policy like control, but skipped instead of failing
	// when the network in front of the cluster already blocks it.
	controlIfReachable bool
}

type egressLab struct {
	ctx         context.Context
	cs          kubernetes.Interface
	client      *Client
	workloads   map[string]*AppWorkload
	targetPodIP string
	targetSvcIP string
	dbPodIP     string
	kubeAPIAddr string
}

func newEgressLab(t *testing.T) *egressLab {
	t.Helper()
	lab := &egressLab{ctx: context.Background()}
	lab.startCluster(t)
	for _, ns := range []string{egressNamespaceA, egressNamespaceB} {
		if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	lab.startTenantB(t)
	// The own-project DB needs no labels: the DB rule opens the namespace by an empty selector.
	runPod(lab.ctx, t, lab.cs, egressNamespaceA, "db", nil, []string{"/agnhost", "netexec", "--http-port=5432"})
	lab.dbPodIP = podIP(lab.ctx, t, lab.cs, egressNamespaceA, "db")
	kubeAPISvc, err := lab.cs.CoreV1().Services("default").Get(lab.ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get kubernetes service: %v", err)
	}
	lab.kubeAPIAddr = kubeAPISvc.Spec.ClusterIP + ":443"
	lab.renderClients(t)
	return lab
}

func (lab *egressLab) startCluster(t *testing.T) {
	t.Helper()
	container, err := k3s.Run(lab.ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	kubeconfig, err := container.GetKubeConfig(lab.ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	lab.cs = cs
	lab.client = &Client{clientset: cs, restConfig: restCfg}
}

func (lab *egressLab) startTenantB(t *testing.T) {
	t.Helper()
	runPod(lab.ctx, t, lab.cs, egressNamespaceB, "target", map[string]string{"role": "target"}, []string{"/agnhost", "netexec", "--http-port=8080"})
	svc, err := lab.cs.CoreV1().Services(egressNamespaceB).Create(lab.ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: egressNamespaceB},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"role": "target"},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080)}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create target service: %v", err)
	}
	lab.targetSvcIP = svc.Spec.ClusterIP
	lab.targetPodIP = podIP(lab.ctx, t, lab.cs, egressNamespaceB, "target")
}

func (lab *egressLab) renderClients(t *testing.T) {
	t.Helper()
	resolver := &fakeResolver{namespace: egressNamespaceA}
	withDB := egressApp("app-with-db", "webdb")
	withDB.Env = []apphost.EnvVar{{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
		SourceKind: apphost.SourceDatabase, SourceName: "proja", Variable: "DATABASE_URL",
	}}}
	apps := map[string]struct {
		app   *apphost.App
		extra []string
	}{
		egressClientWithDB:    {withDB, nil},
		egressClientNoDB:      {egressApp("app-no-db", "webnodb"), nil},
		egressClientExtraDeny: {egressApp("app-extra-deny", "webextra"), []string{egressExtraDeniedCIDR}},
	}
	lab.workloads = map[string]*AppWorkload{}
	for pod, spec := range apps {
		workload, err := RenderAppWorkload(egressNamespaceA, spec.app, resolver, spec.extra...)
		if err != nil {
			t.Fatalf("render %s: %v", pod, err)
		}
		lab.workloads[pod] = workload
		runPod(lab.ctx, t, lab.cs, egressNamespaceA, pod, workload.NetworkPolicy.Spec.PodSelector.MatchLabels, []string{"/bin/sleep", "3600"})
	}
}

func egressApp(id, name string) *apphost.App {
	return &apphost.App{ID: id, ProjectID: "proja", Name: name, Image: "registry.k8s.io/pause:3.10", Port: 8080,
		Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
}

func (lab *egressLab) checks() []egressCheck {
	return []egressCheck{
		{name: "another tenant's pod is refused", pod: egressClientWithDB, addr: lab.targetPodIP + ":8080", control: true},
		{name: "another tenant's service is refused", pod: egressClientWithDB, addr: lab.targetSvcIP + ":8080", control: true},
		{name: "kube API is refused", pod: egressClientWithDB, addr: lab.kubeAPIAddr, control: true},
		{name: "own DB is refused when not referenced", pod: egressClientNoDB, addr: lab.dbPodIP + ":5432", control: true},
		{name: "cloud metadata address is refused", pod: egressClientWithDB, addr: "169.254.169.254:80"},
		{name: "own DB is reachable when referenced", pod: egressClientWithDB, addr: lab.dbPodIP + ":5432", reachable: true},
		{name: "a public address is reachable", pod: egressClientWithDB, addr: egressPublicAddress, reachable: true, needsInternet: true},
		{name: "an extra-deny address is refused", pod: egressClientExtraDeny, addr: egressPublicAddress, needsInternet: true},
		{name: "outbound SMTP port 25 is refused", pod: egressClientWithDB, addr: "smtp.gmail.com:25", controlIfReachable: true},
		{name: "SMTP submission port 587 is reachable", pod: egressClientWithDB, addr: "smtp.gmail.com:587", reachable: true, needsInternet: true},
	}
}

func (lab *egressLab) applyPolicies(t *testing.T) {
	t.Helper()
	for pod, workload := range lab.workloads {
		if _, err := lab.cs.NetworkingV1().NetworkPolicies(egressNamespaceA).Create(lab.ctx, workload.NetworkPolicy, metav1.CreateOptions{}); err != nil {
			t.Fatalf("apply policy for %s: %v", pod, err)
		}
	}
}

func (lab *egressLab) connect(pod, addr string) error {
	_, err := lab.client.ExecInPod(lab.ctx, egressNamespaceA, pod, "main", []string{"/agnhost", "connect", "--timeout=3s", addr})
	return err
}

func runPod(ctx context.Context, t *testing.T, cs kubernetes.Interface, namespace, name string, labels map[string]string, command []string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.PodSpec{
			Containers:    []corev1.Container{{Name: "main", Image: agnhostImage, Command: command}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s/%s: %v", namespace, name, err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		got, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && got.Status.Phase == corev1.PodRunning {
			ready := len(got.Status.ContainerStatuses) > 0
			for _, cst := range got.Status.ContainerStatuses {
				ready = ready && cst.Ready
			}
			if ready {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod %s/%s not ready in time", namespace, name)
		}
		time.Sleep(2 * time.Second)
	}
}

func podIP(ctx context.Context, t *testing.T, cs kubernetes.Interface, namespace, name string) string {
	t.Helper()
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod %s/%s: %v", namespace, name, err)
	}
	if pod.Status.PodIP == "" {
		t.Fatalf("pod %s/%s has no IP", namespace, name)
	}
	return pod.Status.PodIP
}

func eventually(t *testing.T, desc string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s", desc, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

//go:build live

package k8s

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"github.com/testcontainers/testcontainers-go/wait"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// The newest Cilium line whose tested Kubernetes range includes the pinned k3s 1.31.
	ciliumChartVersion = "1.18.14"
	ciliumChartRepo    = "https://helm.cilium.io"
	egressClientNoDeny = "client-no-deny"
	kubeletPort        = "10250"
	kubeAPIServerPort  = "6443"
)

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3sCiliumAppEgress -v -count=1 -timeout 25m
func TestK3sCiliumAppEgress(t *testing.T) {
	// Its fence has no deny rules at all, so a refused node proves the world entity excludes it.
	noDeny := map[string]egressClient{egressClientNoDeny: {app: egressApp("app-no-deny", "webnodeny")}}
	lab := newEgressLab(t, (*egressLab).startCiliumCluster, noDeny)
	checks := append(lab.checks(), lab.nodeChecks()...)
	runEgressChecks(t, lab, checks, lab.applyCiliumPolicies)
}

func (lab *egressLab) nodeChecks() []egressCheck {
	kubelet := net.JoinHostPort(lab.nodeIP, kubeletPort)
	apiServer := net.JoinHostPort(lab.nodeIP, kubeAPIServerPort)
	return []egressCheck{
		{name: "the node's kubelet is refused", pod: egressClientWithDB, addr: kubelet, control: true},
		{name: "the node's API server port is refused", pod: egressClientWithDB, addr: apiServer, control: true},
		{name: "the node's kubelet is refused with no deny rules", pod: egressClientNoDeny, addr: kubelet, control: true},
		{name: "the node's API server port is refused with no deny rules", pod: egressClientNoDeny, addr: apiServer, control: true},
		{name: "a public address is reachable with no deny rules", pod: egressClientNoDeny, addr: egressPublicAddress, reachable: true, needsInternet: true},
	}
}

// applyCiliumPolicies applies each rendered fence through the same code path a deploy uses.
func (lab *egressLab) applyCiliumPolicies(t *testing.T) {
	t.Helper()
	for pod := range lab.clients {
		policy := lab.render(t, pod).EgressPolicy
		if pod == egressClientNoDeny {
			unstructured.RemoveNestedField(policy.Object, "spec", "egressDeny")
		}
		if err := lab.client.applyAppPolicy(lab.ctx, egressNamespaceA, policy, "egress"); err != nil {
			t.Fatalf("apply policy for %s: %v", pod, err)
		}
	}
}

// startCiliumCluster is k3s with its own CNI and policy controller off, and Cilium installed from its Helm chart.
func (lab *egressLab) startCiliumCluster(t *testing.T) {
	t.Helper()
	lab.startCiliumClusterWith(t)
}

func (lab *egressLab) startCiliumClusterWith(t *testing.T, extra ...testcontainers.ContainerCustomizer) {
	t.Helper()
	options := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithCmdArgs("--flannel-backend=none", "--disable-network-policy"),
		// The module waits for a node sync that never comes while no CNI is installed.
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForLog("k3s is up and running").WithStartupTimeout(3*time.Minute)),
	}, extra...)
	container, err := k3s.Run(lab.ctx, "rancher/k3s:v1.31.6-k3s1", options...)
	// Registered before the error check: a container that failed to become ready still runs.
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	// Cilium's pods mount /sys/fs/bpf with propagation, which a docker container's private root refuses.
	if code, _, err := container.Exec(lab.ctx, []string{"mount", "--make-rshared", "/"}); err != nil || code != 0 {
		t.Fatalf("make the k3s root mount shared: exit %d: %v", code, err)
	}
	kubeconfig, err := container.GetKubeConfig(lab.ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	// The Helm SDK reads its cluster from KUBECONFIG.
	kubeconfigFile := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfigFile, kubeconfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", kubeconfigFile)
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	lab.useCluster(t, restCfg)
	nodeIP, err := container.ContainerIP(lab.ctx)
	if err != nil {
		t.Fatalf("k3s container IP: %v", err)
	}
	lab.installCilium(t, nodeIP)
}

func (lab *egressLab) installCilium(t *testing.T, apiServerHost string) {
	t.Helper()
	locate := action.ChartPathOptions{RepoURL: ciliumChartRepo, Version: ciliumChartVersion}
	chartPath, err := locate.LocateChart("cilium", cli.New())
	if err != nil {
		t.Fatalf("fetch cilium chart %s: %v", ciliumChartVersion, err)
	}
	values := map[string]interface{}{
		"ipam":           map[string]interface{}{"mode": "kubernetes"},
		"k8sServiceHost": apiServerHost,
		"k8sServicePort": kubeAPIServerPort,
		"operator":       map[string]interface{}{"replicas": 1},
	}
	if err := lab.client.InstallHelmChart(lab.ctx, "kube-system", "cilium", chartPath, values); err != nil {
		t.Fatalf("install cilium: %v", err)
	}
	// Pods only get addresses once CoreDNS, scheduled before any CNI existed, is running on Cilium.
	eventually(t, "CoreDNS ready on Cilium", 5*time.Minute, func() bool {
		coredns, err := lab.cs.AppsV1().Deployments("kube-system").Get(lab.ctx, "coredns", metav1.GetOptions{})
		return err == nil && deploymentAvailable(coredns)
	})
}

func deploymentAvailable(deployment *appsv1.Deployment) bool {
	return deployment.Spec.Replicas != nil && deployment.Status.AvailableReplicas == *deployment.Spec.Replicas
}

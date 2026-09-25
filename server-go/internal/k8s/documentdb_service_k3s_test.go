//go:build live

package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	gatewayLabNamespace = "org1-gwproj"
	gatewayLabProject   = "gw"
	gatewayLabCluster   = gatewayLabProject + postgresSuffix
	cnpgManifest        = "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml"
	// A stand-in for the gateway: the real one needs the operator's plugin support, which no test cluster here runs.
	gatewayStandIn = `import socket, os
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", 10260))
s.listen()
while True:
    c, _ = s.accept()
    c.sendall(os.uname().nodename.encode())
    c.close()`
	gatewayDial = `import socket, sys
c = socket.create_connection((sys.argv[1], 10260), 3)
print(c.recv(100).decode())`
)

type gatewayLab struct {
	ctx    context.Context
	client *Client
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sDocumentDBServiceFollowsThePrimary -v -count=1 -timeout 20m
func TestK3sDocumentDBServiceFollowsThePrimary(t *testing.T) {
	lab := startGatewayLab(t)
	lab.startCluster(t)
	if err := lab.client.EnsureDocumentDBService(lab.ctx, gatewayLabNamespace, gatewayLabProject); err != nil {
		t.Fatalf("EnsureDocumentDBService: %v", err)
	}
	t.Logf("read-write selector copied: %v", lab.serviceSelector(t))

	first := lab.currentPrimary(t)
	lab.startStandIns(t)
	lab.expectAnswerFrom(t, first)

	second := gatewayLabCluster + "-2"
	if first == second {
		second = gatewayLabCluster + "-1"
	}
	lab.switchover(t, second)
	lab.startStandIns(t)
	lab.expectAnswerFrom(t, second)
}

func startGatewayLab(t *testing.T) *gatewayLab {
	t.Helper()
	ctx := context.Background()
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	if code, _, err := container.Exec(ctx, []string{"kubectl", "apply", "--server-side", "-f", cnpgManifest}); err != nil || code != 0 {
		t.Fatalf("install CNPG: exit %d: %v", code, err)
	}
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	egress := &egressLab{ctx: ctx}
	egress.useCluster(t, restCfg)
	lab := &gatewayLab{ctx: ctx, client: egress.client}
	eventually(t, "CNPG operator available", 4*time.Minute, func() bool {
		operator, err := egress.cs.AppsV1().Deployments("cnpg-system").Get(ctx, "cnpg-controller-manager", metav1.GetOptions{})
		return err == nil && deploymentAvailable(operator)
	})
	return lab
}

func (lab *gatewayLab) startCluster(t *testing.T) {
	t.Helper()
	if err := lab.client.CreateProjectNamespace(lab.ctx, gatewayLabNamespace, "org1"); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: gatewayLabProject,
		Namespace: gatewayLabNamespace,
		Tier:      config.TierConfig{Instances: 2, StorageSize: "1Gi", Memory: "256Mi", CPU: "0.25"},
	})
	eventually(t, "cluster accepted by the operator webhook", 2*time.Minute, func() bool {
		return lab.client.ApplyCRD(lab.ctx, CNPGClusterGVR, gatewayLabNamespace, cluster) == nil
	})
	for _, pod := range []string{gatewayLabCluster + "-1", gatewayLabCluster + "-2"} {
		eventually(t, pod+" ready", 8*time.Minute, func() bool {
			ready, _ := lab.client.IsPodReady(lab.ctx, gatewayLabNamespace, pod)
			return ready
		})
	}
}

func (lab *gatewayLab) serviceSelector(t *testing.T) map[string]string {
	t.Helper()
	svc, err := lab.client.clientset.CoreV1().Services(gatewayLabNamespace).
		Get(lab.ctx, DocumentDBServiceName(gatewayLabProject), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read gateway service: %v", err)
	}
	return svc.Spec.Selector
}

func (lab *gatewayLab) currentPrimary(t *testing.T) string {
	t.Helper()
	cluster, err := lab.client.GetCRD(lab.ctx, CNPGClusterGVR, gatewayLabNamespace, gatewayLabCluster)
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		t.Fatal("cluster reports no current primary")
	}
	return primary
}

// startStandIns listens on the gateway port in every instance; a second start on a pod still listening just fails to bind.
func (lab *gatewayLab) startStandIns(t *testing.T) {
	t.Helper()
	for _, pod := range []string{gatewayLabCluster + "-1", gatewayLabCluster + "-2"} {
		cmd := []string{"sh", "-c", `nohup python3 -c "$0" </dev/null >/dev/null 2>&1 &`, gatewayStandIn}
		if _, err := lab.client.ExecInPod(lab.ctx, gatewayLabNamespace, pod, "postgres", cmd); err != nil {
			t.Fatalf("start the stand-in in %s: %v", pod, err)
		}
	}
}

// expectAnswerFrom dials the Service from inside the cluster and checks which instance answered.
func (lab *gatewayLab) expectAnswerFrom(t *testing.T, want string) {
	t.Helper()
	host := DocumentDBServiceHost(gatewayLabProject, gatewayLabNamespace)
	var last string
	eventually(t, "the gateway service reaches "+want, 2*time.Minute, func() bool {
		out, err := lab.client.ExecInPod(lab.ctx, gatewayLabNamespace, gatewayLabCluster+"-1", "postgres",
			[]string{"python3", "-c", gatewayDial, host})
		last = strings.TrimSpace(out)
		if err != nil {
			last = err.Error()
		}
		return last == want
	})
	t.Logf("%s:%d answered by %s", host, config.DocumentDBGatewayPort, last)
}

// switchover asks the operator to promote target, the way its kubectl plugin does.
func (lab *gatewayLab) switchover(t *testing.T, target string) {
	t.Helper()
	clusters := lab.client.dynamicClient.Resource(CNPGClusterGVR).Namespace(gatewayLabNamespace)
	cluster, err := clusters.Get(lab.ctx, gatewayLabCluster, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	status := map[string]interface{}{
		"targetPrimary":          target,
		"targetPrimaryTimestamp": time.Now().UTC().Format(time.RFC3339),
		"phase":                  "Switchover in progress",
		"phaseReason":            "Switching over to " + target,
	}
	for key, value := range status {
		if err := unstructured.SetNestedField(cluster.Object, value, "status", key); err != nil {
			t.Fatalf("set status %s: %v", key, err)
		}
	}
	if _, err := clusters.UpdateStatus(lab.ctx, cluster, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("request switchover: %v", err)
	}
	eventually(t, target+" is primary", 5*time.Minute, func() bool { return lab.currentPrimary(t) == target })
	eventually(t, target+" ready", 5*time.Minute, func() bool {
		ready, _ := lab.client.IsPodReady(lab.ctx, gatewayLabNamespace, target)
		return ready
	})
}

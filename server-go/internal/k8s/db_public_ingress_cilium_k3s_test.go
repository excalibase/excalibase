//go:build live

package k8s

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	publicDBNamespace      = "org1-pubdb"
	publicDBOtherTenant    = "org2-other"
	publicDBControlNS      = "edge-control"
	publicDBProject        = "pubdb"
	publicDBNodePort       = 30432
	publicMongoNodePort    = 30260
	publicControlNodePort  = 30433
	publicDBStaysRefusedIn = 20 * time.Second
)

type publicDBLab struct {
	egress *egressLab
	dbIP   string
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumPublicDBIngress -v -count=1 -timeout 25m
// A NodePort stands in for the MetalLB LoadBalancer: outside traffic takes the same node-to-pod path.
func TestK3sCiliumPublicDBIngress(t *testing.T) {
	lab := startPublicDBLab(t)
	ctx := lab.egress.ctx
	client := lab.egress.client

	lab.expectOutside(t, "the edge path works for an unfenced namespace", publicControlNodePort, true)
	lab.expectStaysRefused(t, publicDBNodePort)
	lab.expectStaysRefused(t, publicMongoNodePort)

	if err := client.EnsurePublicDBIngressPolicy(ctx, publicDBNamespace, publicDBProject, []int{postgresPort, config.DocumentDBGatewayPort}); err != nil {
		t.Fatalf("EnsurePublicDBIngressPolicy: %v", err)
	}
	lab.expectOutside(t, "postgres is reachable from outside", publicDBNodePort, true)
	lab.expectOutside(t, "the gateway is reachable from outside", publicMongoNodePort, true)
	lab.expectOtherTenantRefused(t)

	if err := client.DeletePublicDBIngressPolicy(ctx, publicDBNamespace, publicDBProject); err != nil {
		t.Fatalf("DeletePublicDBIngressPolicy: %v", err)
	}
	lab.expectOutside(t, "postgres is refused again", publicDBNodePort, false)
	lab.expectOutside(t, "the gateway is refused again", publicMongoNodePort, false)
}

func startPublicDBLab(t *testing.T) *publicDBLab {
	t.Helper()
	egress := &egressLab{ctx: context.Background()}
	egress.startCiliumCluster(t)
	egress.nodeIP = nodeInternalIP(egress.ctx, t, egress.cs)
	lab := &publicDBLab{egress: egress}
	for _, ns := range []string{publicDBNamespace, publicDBOtherTenant} {
		if err := egress.client.CreateProjectNamespace(egress.ctx, ns, "org1"); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	if _, err := egress.cs.CoreV1().Namespaces().Create(egress.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: publicDBControlNS}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create control namespace: %v", err)
	}
	lab.startDatabase(t)
	runPod(egress.ctx, t, egress.cs, publicDBControlNS, "control", map[string]string{"role": "control"}, []string{"/agnhost", "netexec", "--http-port=5432"})
	lab.nodePort(t, publicDBControlNS, map[string]string{"role": "control"}, map[int32]int32{publicControlNodePort: postgresPort})
	runPod(egress.ctx, t, egress.cs, publicDBOtherTenant, "client", nil, []string{"/bin/sleep", "3600"})
	return lab
}

func (lab *publicDBLab) startDatabase(t *testing.T) {
	t.Helper()
	ctx, cs := lab.egress.ctx, lab.egress.cs
	labels := map[string]string{"cnpg.io/cluster": publicDBProject + postgresSuffix, "cnpg.io/podRole": "instance"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: publicDBNamespace, Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "postgres", Image: agnhostImage, Command: []string{"/agnhost", "netexec", "--http-port=5432"}},
			{Name: "gateway", Image: agnhostImage, Command: []string{"/agnhost", "netexec", "--http-port=10260", "--udp-port=8082"}},
		}},
	}
	if _, err := cs.CoreV1().Pods(publicDBNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create database pod: %v", err)
	}
	eventually(t, "database pod ready", 3*time.Minute, func() bool {
		ready, _ := lab.egress.client.IsPodReady(ctx, publicDBNamespace, "db")
		return ready
	})
	lab.dbIP = podIP(ctx, t, cs, publicDBNamespace, "db")
	lab.nodePort(t, publicDBNamespace, labels, map[int32]int32{publicDBNodePort: postgresPort, publicMongoNodePort: config.DocumentDBGatewayPort})
}

func (lab *publicDBLab) nodePort(t *testing.T, namespace string, selector map[string]string, ports map[int32]int32) {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: namespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Selector: selector},
	}
	for nodePort, target := range ports {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name: strconv.Itoa(int(nodePort)), Port: nodePort, NodePort: nodePort, TargetPort: intstr.FromInt32(target),
		})
	}
	if _, err := lab.egress.cs.CoreV1().Services(namespace).Create(lab.egress.ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create node port service in %s: %v", namespace, err)
	}
}

func (lab *publicDBLab) dialOutside(port int) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(lab.egress.nodeIP, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (lab *publicDBLab) expectOutside(t *testing.T, desc string, port int, reachable bool) {
	t.Helper()
	eventually(t, desc, 60*time.Second, func() bool { return (lab.dialOutside(port) == nil) == reachable })
	t.Logf("%s: port %d reachable=%v", desc, port, reachable)
}

// expectStaysRefused fails on any successful dial within the window, so a refusal is not just the path still being set up.
func (lab *publicDBLab) expectStaysRefused(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(publicDBStaysRefusedIn)
	for time.Now().Before(deadline) {
		if lab.dialOutside(port) == nil {
			t.Fatalf("port %d is reachable from outside before the endpoint is public", port)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("port %d refused from outside before the endpoint is public", port)
}

func (lab *publicDBLab) expectOtherTenantRefused(t *testing.T) {
	t.Helper()
	control := net.JoinHostPort(podIP(lab.egress.ctx, t, lab.egress.cs, publicDBControlNS, "control"), strconv.Itoa(postgresPort))
	if _, err := lab.egress.client.ExecInPod(lab.egress.ctx, publicDBOtherTenant, "client", "main",
		[]string{"/agnhost", "connect", "--timeout=3s", control}); err != nil {
		t.Fatalf("the other tenant's pod cannot reach an unfenced pod either, so a refusal proves nothing: %v", err)
	}
	for _, port := range []int{postgresPort, config.DocumentDBGatewayPort} {
		addr := net.JoinHostPort(lab.dbIP, strconv.Itoa(port))
		_, err := lab.egress.client.ExecInPod(lab.egress.ctx, publicDBOtherTenant, "client", "main",
			[]string{"/agnhost", "connect", "--timeout=3s", addr})
		if err == nil {
			t.Errorf("another tenant's pod reached the database at %s", addr)
		}
	}
}

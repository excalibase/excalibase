//go:build live

package k8s

import (
	"context"
	"maps"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	allowListNamespace = "org1-allowdb"
	allowListProject   = "allowdb"
	// TEST-NET-1: no client of this test ever has it.
	allowListStranger = "192.0.2.0/24"
)

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumPublicDBAllowList -v -count=1 -timeout 25m
//
// The production datapath (Cilium replacing kube-proxy) drops a source outside
// loadBalancerSourceRanges. There is no load balancer here, so the Service's
// NodePort stands in for the address and bpf.lbSourceRangeAllTypes applies
// the list to it, as Cilium applies it to the MetalLB address in production.
func TestK3sCiliumPublicDBAllowList(t *testing.T) {
	values := maps.Clone(productionCiliumValues)
	values["bpf"] = map[string]interface{}{"lbSourceRangeAllTypes": true}
	lab := &egressLab{ctx: context.Background(), ciliumValues: values}
	// Without kube-proxy and servicelb, only Cilium can be what enforces the list.
	lab.startCiliumClusterWith(t, testcontainers.WithCmdArgs("--disable-kube-proxy", "--disable=traefik", "--disable=servicelb"))
	lab.nodeIP = nodeInternalIP(lab.ctx, t, lab.cs)
	ctx, client := lab.ctx, lab.client

	if err := client.CreateProjectNamespace(ctx, allowListNamespace, "org1"); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	selector := PostgresInstanceSelector(allowListProject)
	runPod(ctx, t, lab.cs, allowListNamespace, "db", selector, []string{"/agnhost", "netexec", "--http-port=5432"})
	readWrite := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: allowListProject + "-postgres-rw", Namespace: allowListNamespace},
		Spec:       corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{{Port: postgresPort}}},
	}
	if _, err := lab.cs.CoreV1().Services(allowListNamespace).Create(ctx, readWrite, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create read-write service: %v", err)
	}
	if err := client.EnsurePublicDBIngressPolicy(ctx, allowListNamespace, allowListProject, selector, []int{postgresPort}); err != nil {
		t.Fatalf("EnsurePublicDBIngressPolicy: %v", err)
	}

	ours := hostNetwork(t, lab.nodeIP)
	spec := PublicDBServiceSpec{
		Name: allowListProject + "-postgres-public", Port: 30500, ReadWriteService: readWrite.Name,
		SharedIPKey: "excalibase-db-edge", ProjectID: allowListProject,
	}
	ensure := func(ranges []string) int {
		spec.SourceRanges = ranges
		if err := client.EnsurePublicDBService(ctx, allowListNamespace, spec); err != nil {
			t.Fatalf("EnsurePublicDBService %v: %v", ranges, err)
		}
		return publicNodePort(t, lab, spec.Name)
	}

	port := ensure(nil)
	eventually(t, "no list admits any source", 90*time.Second, func() bool { return dialNode(lab.nodeIP, port) == nil })
	port = ensure([]string{allowListStranger, "2001:db8::/32"})
	eventually(t, "a source outside the list is refused", 60*time.Second, func() bool { return dialNode(lab.nodeIP, port) != nil })
	stayRefused(t, lab.nodeIP, port, "a source outside the list")
	port = ensure([]string{ours})
	eventually(t, "a listed source is admitted", 60*time.Second, func() bool { return dialNode(lab.nodeIP, port) == nil })

	spec.NoNodePorts = true
	if err := client.EnsurePublicDBService(ctx, allowListNamespace, spec); err != nil {
		t.Fatalf("EnsurePublicDBService without node ports: %v", err)
	}
	svc, err := lab.cs.CoreV1().Services(allowListNamespace).Get(ctx, spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	if svc.Spec.Ports[0].NodePort != 0 {
		t.Fatalf("node port %d is still allocated; it would answer outside the list", svc.Spec.Ports[0].NodePort)
	}
	eventually(t, "the released node port stops answering", 60*time.Second, func() bool { return dialNode(lab.nodeIP, port) != nil })
	stayRefused(t, lab.nodeIP, port, "the released node port")
}

// hostNetwork is the docker network the node sits on, which holds the address
// this test dials from.
func hostNetwork(t *testing.T, nodeIP string) string {
	t.Helper()
	addr, err := netip.ParseAddr(nodeIP)
	if err != nil {
		t.Fatalf("node IP %q: %v", nodeIP, err)
	}
	prefix, err := addr.Prefix(16)
	if err != nil {
		t.Fatalf("prefix of %s: %v", nodeIP, err)
	}
	return prefix.String()
}

func publicNodePort(t *testing.T, lab *egressLab, name string) int {
	t.Helper()
	svc, err := lab.cs.CoreV1().Services(allowListNamespace).Get(lab.ctx, name, metav1.GetOptions{})
	if err != nil || len(svc.Spec.Ports) == 0 || svc.Spec.Ports[0].NodePort == 0 {
		t.Fatalf("service %s has no node port: %v", name, err)
	}
	return int(svc.Spec.Ports[0].NodePort)
}

func dialNode(nodeIP string, port int) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(nodeIP, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func stayRefused(t *testing.T, nodeIP string, port int, desc string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if dialNode(nodeIP, port) == nil {
			t.Fatalf("%s reached port %d", desc, port)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("%s refused on port %d", desc, port)
}

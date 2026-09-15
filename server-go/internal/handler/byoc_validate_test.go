package handler

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/byoc"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// tableResolver is a fixed DNS table for handler tests so the BYOC path never
// touches real DNS. Unknown names fail like NXDOMAIN.
type tableResolver map[string][]string

func (t tableResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	answer, ok := t[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]netip.Addr, 0, len(answer))
	for _, s := range answer {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func newBYOCTestHandler(policy byoc.Policy, resolver byoc.Resolver) *ProvisioningHandler {
	h := &ProvisioningHandler{}
	h.SetEgressGuard(byoc.NewGuard(policy, resolver))
	return h
}

func TestValidateBYOCHost(t *testing.T) {
	h := newBYOCTestHandler(byoc.Policy{}, tableResolver{
		"db.example.com":  {"203.0.113.10"},
		"my-host.acme.io": {"203.0.113.10"},
	})
	ctx := context.Background()

	bad := []string{
		"",                // empty
		"localhost",       // loopback name
		"db.localhost",    // .localhost suffix
		"svc.internal",    // .internal suffix
		"node.local",      // .local suffix
		"127.0.0.1",       // loopback IP
		"169.254.169.254", // cloud metadata
		"10.1.2.3",        // private range
		"192.168.0.5",     // private range
		"172.16.0.1",      // private range
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"169.254.10.10",   // link-local
		"::1",             // v6 loopback
		"fd00:ec2::254",   // v6 metadata (ULA)
		"fe80::1",         // v6 link-local
		"::ffff:10.0.0.1", // v4-mapped private
	}
	for _, host := range bad {
		if err := h.validateBYOCHost(ctx, host); err == nil {
			t.Errorf("expected %q to be rejected", host)
		}
	}

	good := []string{
		"db.example.com",  // public DNS name
		"8.8.8.8",         // public IP
		"203.0.113.10",    // public IP (TEST-NET-3 but routable form)
		"my-host.acme.io", // public DNS
		"2001:4860:4860::8888",
	}
	for _, host := range good {
		if err := h.validateBYOCHost(ctx, host); err != nil {
			t.Errorf("expected %q to be accepted, got %v", host, err)
		}
	}
}

// TestValidateBYOCHost_DNSRebindingBlocked verifies that a DNS name resolving
// to an internal/metadata IP is rejected at validation time, while a name
// resolving to a public IP is accepted. Dial-time enforcement lives in the
// byoc package tests.
func TestValidateBYOCHost_DNSRebindingBlocked(t *testing.T) {
	h := newBYOCTestHandler(byoc.Policy{}, tableResolver{
		"rebind.attacker.example":   {"169.254.169.254"},          // metadata
		"private.attacker.example":  {"10.0.0.5"},                 // RFC-1918
		"loopback.attacker.example": {"203.0.113.9", "127.0.0.1"}, // one bad addr among good
		"public.good.example":       {"203.0.113.10"},             // routable
	})
	ctx := context.Background()

	rejected := []string{
		"rebind.attacker.example",
		"private.attacker.example",
		"loopback.attacker.example", // any internal addr in the set blocks
		"broken.example",            // resolution failure → fail closed
	}
	for _, host := range rejected {
		if err := h.validateBYOCHost(ctx, host); err == nil {
			t.Errorf("expected %q to be rejected (resolves internal / unresolvable)", host)
		}
	}

	if err := h.validateBYOCHost(ctx, "public.good.example"); err != nil {
		t.Errorf("expected public host to be accepted, got %v", err)
	}
}

func TestProvisioningHandler_DefaultGuardWhenNoneWired(t *testing.T) {
	h := &ProvisioningHandler{}
	if err := h.validateBYOCHost(context.Background(), "127.0.0.1"); err == nil {
		t.Error("handler without an explicit guard must still block loopback")
	}
}

func TestIsDenoPodReady(t *testing.T) {
	mock := k8s.NewMockClient()
	ns := "proj-deno"
	mock.Pods[ns] = []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "deno-runtime-abc"}},
	}
	mock.PodReady[ns+"/deno-runtime-abc"] = true

	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	h.SetK8sClient(mock, "img", "secret")

	if !h.isDenoPodReady(context.Background(), ns) {
		t.Error("expected deno pod to be reported ready")
	}

	// Different namespace with no matching pod → not ready.
	if h.isDenoPodReady(context.Background(), "empty-ns") {
		t.Error("empty namespace should not be ready")
	}
}

func TestWaitForDenoReady_EarlyReturnWhenRuntimeURLFnSet(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	// SetRuntimeURLFn makes waitForDenoReady a no-op (test/subprocess path).
	h.SetRuntimeURLFn(func(string) string { return "http://localhost:9" })
	if err := h.waitForDenoReady(context.Background(), "ns"); err != nil {
		t.Errorf("with runtimeURLFn set, waitForDenoReady should be a no-op, got %v", err)
	}
}

func TestWaitForDenoReady_NoK8sClientIsNoOp(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	// k8sClient nil → no-op.
	if err := h.waitForDenoReady(context.Background(), "ns"); err != nil {
		t.Errorf("with nil k8sClient, waitForDenoReady should be a no-op, got %v", err)
	}
}

func TestWaitForDenoReady_HappyPath(t *testing.T) {
	mock := k8s.NewMockClient()
	ns := "proj-ready"
	mock.Pods[ns] = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "deno-runtime-1"}}}
	mock.PodReady[ns+"/deno-runtime-1"] = true

	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	h.SetK8sClient(mock, "img", "secret")
	if err := h.waitForDenoReady(context.Background(), ns); err != nil {
		t.Errorf("ready pod should make waitForDenoReady return nil, got %v", err)
	}
}

func TestOrgSlugFor_NilStores(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	if _, ok := h.orgSlugFor(context.Background(), "proj"); ok {
		t.Error("orgSlugFor with nil stores should return false")
	}
}

func TestIsDenoPodReady_PodNotReady(t *testing.T) {
	mock := k8s.NewMockClient()
	ns := "proj-x"
	mock.Pods[ns] = []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "deno-runtime-xyz"}},
	}
	mock.PodReady[ns+"/deno-runtime-xyz"] = false

	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	h.SetK8sClient(mock, "img", "secret")
	if h.isDenoPodReady(context.Background(), ns) {
		t.Error("pod marked not-ready should report false")
	}
}

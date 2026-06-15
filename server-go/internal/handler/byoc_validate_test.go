package handler

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateBYOCHost(t *testing.T) {
	// Stub DNS so public hostnames resolve to a routable address without
	// depending on real network resolution in CI.
	orig := lookupHost
	t.Cleanup(func() { lookupHost = orig })
	lookupHost = func(host string) ([]string, error) {
		return []string{"203.0.113.10"}, nil
	}

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
	}
	for _, h := range bad {
		if err := validateBYOCHost(h); err == nil {
			t.Errorf("expected %q to be rejected", h)
		}
	}

	good := []string{
		"db.example.com",  // public DNS name
		"8.8.8.8",         // public IP
		"203.0.113.10",    // public IP (TEST-NET-3 but routable form)
		"my-host.acme.io", // public DNS
	}
	for _, h := range good {
		if err := validateBYOCHost(h); err != nil {
			t.Errorf("expected %q to be accepted, got %v", h, err)
		}
	}
}

// TestValidateBYOCHost_DNSRebindingBlocked verifies that a DNS name resolving
// to an internal/metadata IP is rejected at validation time (closing the
// DNS-rebinding SSRF gap), while a name resolving to a public IP is accepted.
func TestValidateBYOCHost_DNSRebindingBlocked(t *testing.T) {
	orig := lookupHost
	t.Cleanup(func() { lookupHost = orig })

	stub := map[string][]string{
		"rebind.attacker.example":   {"169.254.169.254"}, // metadata
		"private.attacker.example":  {"10.0.0.5"},        // RFC-1918
		"loopback.attacker.example": {"203.0.113.9", "127.0.0.1"}, // one bad addr among good
		"public.good.example":       {"203.0.113.10"},   // routable
		"broken.example":            nil,                 // resolution failure
	}
	lookupHost = func(host string) ([]string, error) {
		addrs, ok := stub[host]
		if !ok || addrs == nil {
			return nil, fmt.Errorf("no such host")
		}
		return addrs, nil
	}

	rejected := []string{
		"rebind.attacker.example",
		"private.attacker.example",
		"loopback.attacker.example", // any internal addr in the set blocks
		"broken.example",            // resolution failure → fail closed
	}
	for _, host := range rejected {
		if err := validateBYOCHost(host); err == nil {
			t.Errorf("expected %q to be rejected (resolves internal / unresolvable)", host)
		}
	}

	if err := validateBYOCHost("public.good.example"); err != nil {
		t.Errorf("expected public host to be accepted, got %v", err)
	}
}

// TestClassifyBYOCIP asserts the shared IP-classification logic directly so the
// block-list stays correct independent of the resolution path.
func TestClassifyBYOCIP(t *testing.T) {
	bad := []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.0.5", "172.16.0.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1", "169.254.10.10"}
	for _, s := range bad {
		if err := classifyBYOCIP(net.ParseIP(s)); err == nil {
			t.Errorf("expected %q to be classified internal/non-routable", s)
		}
	}
	good := []string{"8.8.8.8", "203.0.113.10", "1.1.1.1"}
	for _, s := range good {
		if err := classifyBYOCIP(net.ParseIP(s)); err != nil {
			t.Errorf("expected %q to be classified public, got %v", s, err)
		}
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

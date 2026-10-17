package k8s

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	psaapi "k8s.io/pod-security-admission/api"
)

func fenceInit(t *testing.T, spec corev1.PodSpec) corev1.Container {
	t.Helper()
	if len(spec.InitContainers) != 1 || spec.InitContainers[0].Name != denoFenceContainerName {
		t.Fatalf("want exactly the %s init container, got %+v", denoFenceContainerName, spec.InitContainers)
	}
	return spec.InitContainers[0]
}

// EXC-622: a policy controller may program the fence after the pod starts
// (k3s's kube-router took about 0.6 s), so nothing of the runtime starts
// until the fence answers for it.
func TestDenoRuntime_WaitsForItsFenceBeforeStarting(t *testing.T) {
	spec := DenoRuntimeSpec{Image: "excalibase/deno-runtime:9.9.9", RuntimeSecret: "s"}
	deployment := buildDenoDeployment(testNamespace, spec)
	fence := fenceInit(t, deployment.Spec.Template.Spec)
	if fence.Image != spec.Image {
		t.Errorf("fence image = %q, want the runtime's own %q (nothing else to pull)", fence.Image, spec.Image)
	}
	if len(fence.Command) < 2 || strings.Join(fence.Command[:len(fence.Command)-1], " ") != "deno eval --ext=ts" || fence.Command[len(fence.Command)-1] != denoFenceScript {
		t.Errorf("fence command = %q", fence.Command)
	}
	if len(fence.Env) != 0 || len(fence.EnvFrom) != 0 || len(fence.VolumeMounts) != 0 {
		t.Errorf("the fence needs no secret, env or volume: %+v", fence)
	}
	security := fence.SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
		security.Capabilities == nil || len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
		t.Errorf("fence security context = %+v", security)
	}
	if fence.Resources.Limits.Cpu().IsZero() || fence.Resources.Limits.Memory().IsZero() {
		t.Errorf("fence must be bounded: %+v", fence.Resources)
	}
	template := deployment.Spec.Template
	assertPodMeets(t, psaapi.LevelBaseline, "deno runtime with its fence", &template.ObjectMeta, &template.Spec)
}

// The fence goes in before the pod it guards is asked for.
func TestEnsureDenoRuntime_AppliesTheFenceBeforeTheDeployment(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	fence, deployment := -1, -1
	for i, action := range c.clientset.(*fake.Clientset).Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		switch action.GetResource().Resource {
		case "networkpolicies":
			fence = i
		case "deployments":
			deployment = i
		}
	}
	if fence < 0 || deployment < 0 || fence > deployment {
		t.Fatalf("fence created at action %d, deployment at %d: the fence must come first", fence, deployment)
	}
}

// A runtime made before EXC-622 gets the fence wait on the next deploy.
func TestEnsureDenoRuntime_ExistingRuntimeWithoutTheFenceWaitGetsIt(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL}
	old := buildDenoDeployment(egressNS, spec)
	old.Spec.Template.Spec.InitContainers = nil
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	fenceInit(t, denoDeployment(t, c).Spec.Template.Spec)
	if n := countActions(c, "update", "deployments"); n != 1 {
		t.Fatalf("want one deployment update, got %d", n)
	}
}

// The script itself, run by the deno binary when this host has one.
func TestDenoFenceScript(t *testing.T) {
	deno, err := exec.LookPath("deno")
	if err != nil {
		t.Skip("no deno binary on this host")
	}
	answering := listen(t)
	closed := closedAddress(t)
	t.Run("passes once the canary is refused and DNS answers", func(t *testing.T) {
		output, err := runFenceScript(t, deno, closed, answering, "5000")
		if err != nil {
			t.Fatalf("want a pass, got %v:\n%s", err, output)
		}
		if !strings.Contains(output, "egress fence is in place") {
			t.Errorf("output = %q", output)
		}
	})
	t.Run("fails, saying why, while the canary still answers", func(t *testing.T) {
		start := time.Now()
		output, err := runFenceScript(t, deno, answering, answering, "1500")
		if err == nil {
			t.Fatalf("want a failure, got a pass:\n%s", output)
		}
		if !strings.Contains(output, "egress fence is not in place") || !strings.Contains(output, answering) {
			t.Errorf("output = %q", output)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("took %s, want about the 1.5 s deadline", elapsed)
		}
	})
	t.Run("does not pass while the network is down", func(t *testing.T) {
		output, err := runFenceScript(t, deno, closed, closed, "1500")
		if err == nil || !strings.Contains(output, "DNS") {
			t.Fatalf("want a failure naming DNS, got %v:\n%s", err, output)
		}
	})
}

func runFenceScript(t *testing.T, deno, canary, dns, deadlineMillis string) (string, error) {
	t.Helper()
	host, port, _ := net.SplitHostPort(canary)
	command := exec.Command(deno, "eval", "--ext=ts", denoFenceScript)
	command.Env = append(os.Environ(), "KUBERNETES_SERVICE_HOST="+host, "KUBERNETES_SERVICE_PORT="+port,
		denoFenceDNSEnv+"="+dns, denoFenceDeadlineEnv+"="+deadlineMillis)
	output, err := command.CombinedOutput()
	return string(output), err
}

func listen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener.Addr().String()
}

func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

// The fence runs before the runtime, so the scheduler reserves the larger of
// the two: it must never ask for more than the runtime does, or each FREE
// project's functions reserve ten times their size (nightly 39055302492).
func TestDenoFence_ReservesNoMoreThanTheRuntime(t *testing.T) {
	for _, tier := range []string{"FREE", "STANDARD", "ENTERPRISE"} {
		t.Run(tier, func(t *testing.T) {
			deployment := buildDenoDeployment(testNamespace, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", Tier: tier})
			pod := &corev1.Pod{Spec: deployment.Spec.Template.Spec}
			runtime := pod.Spec.Containers[0].Resources.Requests
			cpu, memory := podRequest(pod)
			if cpu != runtime.Cpu().MilliValue() || memory != runtime.Memory().Value() {
				t.Errorf("pod reserves %dm/%d, want the runtime's own %dm/%d", cpu, memory, runtime.Cpu().MilliValue(), runtime.Memory().Value())
			}
			fence := fenceInit(t, pod.Spec)
			for name, request := range fence.Resources.Requests {
				if limit := fence.Resources.Limits[name]; request.Cmp(limit) > 0 {
					t.Errorf("fence %s request %s exceeds its limit %s", name, request.String(), limit.String())
				}
			}
		})
	}
}

// A runtime fenced before the fence was sized to the tier is resized.
func TestEnsureDenoRuntime_OversizedFenceIsResized(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL}
	old := buildDenoDeployment(egressNS, spec)
	oversized := resource.MustParse("100m")
	old.Spec.Template.Spec.InitContainers[0].Resources.Requests[corev1.ResourceCPU] = oversized
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	fence := fenceInit(t, denoDeployment(t, c).Spec.Template.Spec)
	if got := fence.Resources.Requests.Cpu().MilliValue(); got != 10 {
		t.Errorf("fence cpu request = %dm, want the FREE runtime's 10m", got)
	}
}

//go:build live

package k8s

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// EXC-569 live: an allowlist change rolls the function runtime. In that
// window a caller must not be refused, and a deploy must wait for the new pod
// or it is lost with the old one.
//
// Build the runtime image first, then:
//
//	docker build -t excalibase/deno-runtime:exc569 -f deno-server/Dockerfile deno-server
//	DENO_RUNTIME_IMAGE=excalibase/deno-runtime:exc569 \
//	  go test ./internal/k8s/ -tags=live -run TestK3sDenoRuntimeRestartWindow -v -count=1 -timeout 15m
const (
	restartNS     = "proj-restart"
	restartSecret = "live-runtime-secret"
	restartProber = "prober"
	// The prober calls the runtime's Service the way provisioning does, on a
	// new connection each time, for this long.
	restartProbeMs = 60000
)

// proberScript calls the runtime through its Service address every 50ms and
// prints one line per call: "ok", "status N", or "error <message>". The
// address skips cluster DNS, whose hiccups are not what this test measures.
const proberScript = `
const start = Date.now();
const until = start + Number(Deno.env.get("MS"));
const target = "http://" + Deno.env.get("ADDR") + "/scripts";
const headers = { "X-Runtime-Secret": Deno.env.get("S"), "Connection": "close" };
while (Date.now() < until) {
  try {
    const r = await fetch(target, { headers });
    await r.body?.cancel();
    console.log(r.ok ? "ok" : "status " + r.status + " at " + (Date.now() - start) + "ms");
  } catch (e) {
    console.log("error at " + (Date.now() - start) + "ms: " + String(e.message).split("\n")[0]);
  }
  await new Promise((resolve) => setTimeout(resolve, 50));
}
console.log("done");
`

// callScript runs one deploy or invoke against the runtime from inside the
// cluster and prints the status and body.
const callScript = `
const [path, body] = Deno.args;
const r = await fetch("http://" + Deno.env.get("ADDR") + path, {
  method: "POST",
  headers: { "X-Runtime-Secret": Deno.env.get("S"), "Content-Type": "application/json" },
  body,
});
console.log(r.status + " " + (await r.text()));
`

func TestK3sDenoRuntimeRestartWindow(t *testing.T) {
	image := os.Getenv("DENO_RUNTIME_IMAGE")
	if image == "" {
		t.Skip("DENO_RUNTIME_IMAGE names the runtime image under test")
	}
	ctx := context.Background()
	client := startRestartCluster(ctx, t, image)
	spec := DenoRuntimeSpec{Image: image, RuntimeSecret: restartSecret, AllowedHosts: []string{"a.example.com"}}

	if err := client.EnsureDenoRuntime(ctx, restartNS, spec); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	waitRolledOut(ctx, t, client, 3*time.Minute)
	svc, err := client.clientset.CoreV1().Services(restartNS).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("runtime service: %v", err)
	}
	runProbePod(ctx, t, client, restartProber, image, []string{"deno", "eval", proberScript},
		map[string]string{"S": restartSecret, "MS": fmt.Sprint(restartProbeMs), "ADDR": svc.Spec.ClusterIP + ":8000"})
	runProbePod(ctx, t, client, "caller", image, []string{"sleep", "3600"},
		map[string]string{"S": restartSecret, "ADDR": svc.Spec.ClusterIP + ":8000"})
	time.Sleep(3 * time.Second)

	// The change: the fence is rewritten and the pod rolls.
	spec.AllowedHosts = []string{"b.example.com:8443"}
	changed := time.Now()
	if err := client.EnsureDenoRuntime(ctx, restartNS, spec); err != nil {
		t.Fatalf("change allowlist: %v", err)
	}
	state, err := client.DenoRuntimeRollout(ctx, restartNS)
	if err != nil {
		t.Fatal(err)
	}
	if state != DenoRuntimeRollingOut {
		t.Fatalf("right after the change the runtime reports %v; the handler would not wait", state)
	}
	// What the handler did before EXC-569: deploy at once, to the old pod.
	early := runtimeCall(ctx, t, client, "/deploy", `{"id":"early","code":"globalThis.__excalibase_default = () => new Response('early')"}`)
	t.Logf("deploy during the rollout: %s", early)

	waitRolledOut(ctx, t, client, 2*time.Minute)
	t.Logf("rolled out %s after the change", time.Since(changed).Round(time.Millisecond))
	late := runtimeCall(ctx, t, client, "/deploy", `{"id":"late","code":"globalThis.__excalibase_default = () => new Response('late')"}`)
	if !strings.HasPrefix(late, "20") {
		t.Fatalf("deploy after the rollout: %s", late)
	}

	if got := runtimeCall(ctx, t, client, "/invoke/late", `{"method":"GET","url":"/","headers":{},"body":""}`); !strings.Contains(got, "late") {
		t.Fatalf("a deploy made once the rollout finished is gone: %s", got)
	}
	got := runtimeCall(ctx, t, client, "/invoke/early", `{"method":"GET","url":"/","headers":{},"body":""}`)
	t.Logf("invoke of the deploy made during the rollout: %s", got)
	if !strings.Contains(got, "function not found") {
		t.Errorf("expected the deploy made during the rollout to be lost with the old pod, got %s", got)
	}
	assertNoRefusals(t, proberLines(ctx, t, client))
}

func startRestartCluster(ctx context.Context, t *testing.T, image string) *Client {
	t.Helper()
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	if err := container.LoadImages(ctx, image); err != nil {
		t.Fatalf("load %s: %v", image, err)
	}
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	lab := &egressLab{ctx: ctx}
	lab.useCluster(t, restCfg)
	if _, err := lab.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: restartNS}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("namespace: %v", err)
	}
	return lab.client
}

func waitRolledOut(ctx context.Context, t *testing.T, client *Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state, err := client.DenoRuntimeRollout(ctx, restartNS)
		if err == nil && state == DenoRuntimeReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime not rolled out after %s: %v %v", timeout, state, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func runProbePod(ctx context.Context, t *testing.T, client *Client, name, image string, command []string, env map[string]string) {
	t.Helper()
	vars := make([]corev1.EnvVar, 0, len(env))
	for key, value := range env {
		vars = append(vars, corev1.EnvVar{Name: key, Value: value})
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: restartNS},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: command, Env: vars}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if _, err := client.clientset.CoreV1().Pods(restartNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	eventually(t, name+" running", 2*time.Minute, func() bool {
		got, err := client.clientset.CoreV1().Pods(restartNS).Get(ctx, name, metav1.GetOptions{})
		return err == nil && got.Status.Phase == corev1.PodRunning
	})
}

func runtimeCall(ctx context.Context, t *testing.T, client *Client, path, body string) string {
	t.Helper()
	out, err := client.ExecInPod(ctx, restartNS, "caller", "main", []string{"deno", "eval", callScript, path, body})
	if err != nil {
		t.Fatalf("call %s: %v (%s)", path, err, out)
	}
	return strings.TrimSpace(out)
}

// proberLines waits for the prober to finish and returns what it printed.
func proberLines(ctx context.Context, t *testing.T, client *Client) []string {
	t.Helper()
	eventually(t, "prober finished", 3*time.Minute, func() bool {
		got, err := client.clientset.CoreV1().Pods(restartNS).Get(ctx, restartProber, metav1.GetOptions{})
		return err == nil && got.Status.Phase == corev1.PodSucceeded
	})
	raw, err := client.clientset.CoreV1().Pods(restartNS).GetLogs(restartProber, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		t.Fatalf("prober logs: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func assertNoRefusals(t *testing.T, lines []string) {
	t.Helper()
	counts := map[string]int{}
	var failures []string
	for _, line := range lines {
		kind, _, _ := strings.Cut(line, " ")
		counts[kind]++
		if kind == "error" || kind == "status" {
			failures = append(failures, line)
		}
	}
	t.Logf("prober: %v", counts)
	if counts["done"] != 1 || counts["ok"] == 0 {
		t.Fatalf("the prober did not run through: %v", counts)
	}
	if len(failures) > 0 {
		t.Fatalf("%d of %d calls through the Service failed during the rollout, first: %q",
			len(failures), len(lines)-1, failures[0])
	}
}

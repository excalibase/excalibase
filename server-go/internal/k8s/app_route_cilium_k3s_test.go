//go:build live

package k8s

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	haproxyChartRepo    = "https://haproxytech.github.io/helm-charts"
	haproxyChartVersion = "1.54.2"
	haproxyNamespace    = "haproxy-controller"
	haproxyNodePort     = 30080
	routeNamespace      = "org1-proj-routea"
	routeOtherTenant    = "org2-proj-routeb"
	routeAppImage       = "nginxinc/nginx-unprivileged"
	routeRequestsPerSec = 20
	// Covers the last old pod's drain and shutdown after the rollout reports done.
	routeSettleAfterRollout = 20 * time.Second
)

// liveRoute is the route every live render uses; the controller namespace is the one this file installs HAProxy into.
var liveRoute = AppRouteOptions{Domain: "apps.test", IngressClass: "haproxy", IngressFromNamespace: haproxyNamespace}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumAppRoute -v -count=1 -timeout 40m
func TestK3sCiliumAppRoute(t *testing.T) {
	lab := &egressLab{ctx: context.Background()}
	lab.startCiliumClusterWith(t, testcontainers.WithFiles(gvisorFiles(t, gvisorPlatformSystrap)...))
	lab.nodeIP = nodeInternalIP(lab.ctx, t, lab.cs)
	lab.createGVisorRuntimeClass(t)
	for _, ns := range []string{haproxyNamespace, routeNamespace, routeOtherTenant} {
		if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	lab.installHAProxyIngress(t)

	app := routeApp()
	lab.deployRouteApp(t, app)
	route := newRouteProbe(t, lab.nodeIP, app)
	eventually(t, "the app answers through its route", 3*time.Minute, func() bool { return route.get() == nil })

	t.Run("a redeploy to a new env drops no request", func(t *testing.T) {
		app.Env = []apphost.EnvVar{{Name: "REVISION", Kind: apphost.KindLiteral, Value: stringPtr("2")}}
		lab.redeployUnderLoad(t, route, app)
	})
	t.Run("a redeploy to a new image tag drops no request", func(t *testing.T) {
		app.Image = routeAppImage + ":1.28"
		lab.redeployUnderLoad(t, route, app)
	})
	t.Run("another tenant's pod cannot reach the app pod", func(t *testing.T) { lab.checkIngressFence(t, app) })
}

func stringPtr(v string) *string { return &v }

func routeApp() *apphost.App {
	return &apphost.App{ID: "app-route", ProjectID: "proj-routea", Name: "web", Image: routeAppImage + ":1.27",
		Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
}

// createGVisorRuntimeClass: single node, so no scheduling constraint is needed.
func (lab *egressLab) createGVisorRuntimeClass(t *testing.T) {
	t.Helper()
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: gvisorRuntimeClass}, Handler: "runsc"}
	if _, err := lab.cs.NodeV1().RuntimeClasses().Create(lab.ctx, runtimeClass, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create runtime class: %v", err)
	}
}

func (lab *egressLab) installHAProxyIngress(t *testing.T) {
	t.Helper()
	locate := action.ChartPathOptions{RepoURL: haproxyChartRepo, Version: haproxyChartVersion}
	chartPath, err := locate.LocateChart("kubernetes-ingress", cli.New())
	if err != nil {
		t.Fatalf("fetch haproxy chart %s: %v", haproxyChartVersion, err)
	}
	values := map[string]interface{}{"controller": map[string]interface{}{
		"replicaCount":         1,
		"ingressClass":         liveRoute.IngressClass,
		"ingressClassResource": map[string]interface{}{"name": liveRoute.IngressClass},
		"service":              map[string]interface{}{"type": "NodePort", "nodePorts": map[string]interface{}{"http": haproxyNodePort}},
	}}
	if err := lab.client.InstallHelmChart(lab.ctx, haproxyNamespace, "haproxy", chartPath, values); err != nil {
		t.Fatalf("install haproxy ingress: %v", err)
	}
}

// deployRouteApp drives the production path: render, apply, wait.
func (lab *egressLab) deployRouteApp(t *testing.T, app *apphost.App) {
	t.Helper()
	workload, err := RenderAppWorkload(routeNamespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: liveRoute})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := lab.client.ApplyAppWorkload(lab.ctx, routeNamespace, workload); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := lab.client.WaitForAppRollout(lab.ctx, routeNamespace, AppObjectName(app.Name), 5*time.Minute); err != nil {
		t.Fatalf("rollout: %v", err)
	}
}

func (lab *egressLab) redeployUnderLoad(t *testing.T, route *routeProbe, app *apphost.App) {
	t.Helper()
	before := lab.appPodNames(t)
	load := route.startLoad()
	lab.deployRouteApp(t, app)
	time.Sleep(routeSettleAfterRollout)
	total, failures := load.stop()

	after := lab.appPodNames(t)
	for name := range after {
		if before[name] {
			t.Fatalf("pod %s survived the redeploy, so nothing was rolled", name)
		}
	}
	t.Logf("%d requests through the route during the redeploy, %d failed", total, len(failures))
	if len(failures) > 0 {
		t.Errorf("a redeploy dropped %d of %d requests; first: %s", len(failures), total, failures[0])
	}
	if minimum := int64(routeRequestsPerSec * 20); total < minimum {
		t.Errorf("only %d requests were sent, want at least %d for the proof to mean anything", total, minimum)
	}
}

func (lab *egressLab) appPodNames(t *testing.T) map[string]bool {
	t.Helper()
	pods, err := lab.cs.CoreV1().Pods(routeNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/app=app-route"})
	if err != nil {
		t.Fatalf("list app pods: %v", err)
	}
	names := map[string]bool{}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			names[pod.Name] = true
		}
	}
	return names
}

// checkIngressFence proves the refusal is the policy's doing: with it removed the same connection succeeds.
func (lab *egressLab) checkIngressFence(t *testing.T, app *apphost.App) {
	t.Helper()
	runPod(lab.ctx, t, lab.cs, routeOtherTenant, "intruder", nil, []string{"/bin/sleep", "3600"})
	runPod(lab.ctx, t, lab.cs, routeNamespace, "neighbour", nil, []string{"/bin/sleep", "3600"})
	target := net.JoinHostPort(lab.newestAppPodIP(t), strconv.Itoa(app.Port))
	connect := func(namespace, pod string) error {
		_, err := lab.client.ExecInPod(lab.ctx, namespace, pod, "main", []string{"/agnhost", "connect", "--timeout=3s", target})
		return err
	}

	eventually(t, "another tenant is refused", time.Minute, func() bool { return connect(routeOtherTenant, "intruder") != nil })
	eventually(t, "a pod in the app's own namespace is refused", time.Minute, func() bool { return connect(routeNamespace, "neighbour") != nil })

	policies := lab.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(routeNamespace)
	if err := policies.Delete(lab.ctx, AppIngressPolicyName(app.Name), metav1.DeleteOptions{}); err != nil {
		t.Fatalf("remove the ingress policy for the control: %v", err)
	}
	eventually(t, "control: another tenant connects once the policy is gone", time.Minute, func() bool { return connect(routeOtherTenant, "intruder") == nil })
}

func (lab *egressLab) newestAppPodIP(t *testing.T) string {
	t.Helper()
	pods, err := lab.cs.CoreV1().Pods(routeNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/app=app-route"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("list app pods: %v", err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && pod.Status.PodIP != "" {
			return pod.Status.PodIP
		}
	}
	t.Fatal("no live app pod has an IP")
	return ""
}

// routeProbe sends requests to the ingress controller's node port with the app's hostname, as a browser would.
type routeProbe struct {
	client *http.Client
	url    string
	host   string
}

func newRouteProbe(t *testing.T, nodeIP string, app *apphost.App) *routeProbe {
	t.Helper()
	host, err := liveRoute.Public().Hostname(app.Name, app.ProjectID)
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	return &routeProbe{
		client: &http.Client{Timeout: 5 * time.Second},
		url:    "http://" + net.JoinHostPort(nodeIP, strconv.Itoa(haproxyNodePort)) + "/",
		host:   host,
	}
}

func (p *routeProbe) get() error {
	request, err := http.NewRequest(http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	request.Host = p.host
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}

type routeLoad struct {
	done     chan struct{}
	wg       sync.WaitGroup
	total    atomic.Int64
	mu       sync.Mutex
	failures []string
}

// startLoad fires requests at a fixed rate, each on its own goroutine so a slow one never lowers the rate.
func (p *routeProbe) startLoad() *routeLoad {
	load := &routeLoad{done: make(chan struct{})}
	ticker := time.NewTicker(time.Second / routeRequestsPerSec)
	load.wg.Add(1)
	go func() {
		defer load.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-load.done:
				return
			case at := <-ticker.C:
				load.wg.Add(1)
				go load.send(p, at)
			}
		}
	}()
	return load
}

func (l *routeLoad) send(p *routeProbe, at time.Time) {
	defer l.wg.Done()
	l.total.Add(1)
	if err := p.get(); err != nil {
		l.mu.Lock()
		l.failures = append(l.failures, at.Format(time.RFC3339Nano)+": "+err.Error())
		l.mu.Unlock()
	}
}

func (l *routeLoad) stop() (int64, []string) {
	close(l.done)
	l.wg.Wait()
	return l.total.Load(), l.failures
}

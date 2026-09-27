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
	routePlatform       = "excalibase-platform"
	routeDatabasePort   = 5432
	routeAppImage       = "nginxinc/nginx-unprivileged"
	routeRequestsPerSec = 20
	podIntruder         = "intruder"
	podNeighbour        = "neighbour"
	podSecondApp        = "second-app"
	podPlatform         = "platform"
	podDatabase         = "database"
	routeAppLabel       = "excalibase.io/app=app-route"
	// Covers the last old pod's drain and shutdown after the rollout reports done.
	routeSettleAfterRollout = 20 * time.Second
)

// liveRoute is the route every live render uses; the controller namespace is the one this file installs HAProxy into.
var liveRoute = AppRouteOptions{Domain: "apps.test", IngressClass: "haproxy", IngressFromNamespace: haproxyNamespace}

const liveDeployID = "dep-live-1"

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumAppRoute -v -count=1 -timeout 40m
func TestK3sCiliumAppRoute(t *testing.T) {
	lab := &egressLab{ctx: context.Background()}
	lab.startCiliumClusterWith(t, testcontainers.WithFiles(gvisorFiles(t, gvisorPlatformSystrap)...))
	lab.nodeIP = nodeInternalIP(lab.ctx, t, lab.cs)
	lab.createGVisorRuntimeClass(t)
	t.Setenv("POD_NAMESPACE", routePlatform)
	for _, ns := range []string{haproxyNamespace, routePlatform} {
		if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	// Project namespaces are made the production way, so their isolation policies are live.
	for ns, org := range map[string]string{routeNamespace: "org1", routeOtherTenant: "org2"} {
		if err := lab.client.CreateProjectNamespace(lab.ctx, ns, org); err != nil {
			t.Fatalf("create project namespace %s: %v", ns, err)
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
	t.Run("the app accepts only the edge and kubelet probes", func(t *testing.T) { lab.checkIngressFence(t, app, route) })
	t.Run("a rename moves the route and leaves nothing under the old name", func(t *testing.T) {
		app = lab.renameUnderRoute(t, app, route)
		route = newRouteProbe(t, lab.nodeIP, app)
	})
	t.Run("a deleted app stops answering and leaves nothing behind", func(t *testing.T) {
		if err := lab.client.DeleteAppWorkload(lab.ctx, routeNamespace, app.ID, 3*time.Minute); err != nil {
			t.Fatalf("delete: %v", err)
		}
		eventually(t, "the deleted app's URL stops answering", time.Minute, func() bool { return route.get() != nil })
		lab.expectNothingLabelled(t, "excalibase.io/app="+app.ID)
	})
}

// renameUnderRoute deploys the app under a new name the way the deploy
// service does, pruning the old name once the new one has rolled out.
func (lab *egressLab) renameUnderRoute(t *testing.T, app *apphost.App, oldRoute *routeProbe) *apphost.App {
	t.Helper()
	renamed := *app
	renamed.Name = "shop"
	lab.deployRouteApp(t, &renamed)
	if err := lab.client.PruneAppWorkload(lab.ctx, routeNamespace, renamed.ID, renamed.Name, 3*time.Minute); err != nil {
		t.Fatalf("prune: %v", err)
	}
	newRoute := newRouteProbe(t, lab.nodeIP, &renamed)
	eventually(t, "the new name answers", 2*time.Minute, func() bool { return newRoute.get() == nil })
	eventually(t, "the old name stops answering", time.Minute, func() bool { return oldRoute.get() != nil })
	lab.expectNothingLabelled(t, "excalibase.io/app="+app.ID+",app.kubernetes.io/name="+app.Name)
	return &renamed
}

func (lab *egressLab) expectNothingLabelled(t *testing.T, selector string) {
	t.Helper()
	opts := metav1.ListOptions{LabelSelector: selector}
	counts := map[string]int{}
	if l, err := lab.cs.CoreV1().Pods(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["pods"] = len(l.Items)
	}
	if l, err := lab.cs.AppsV1().Deployments(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["deployments"] = len(l.Items)
	}
	if l, err := lab.cs.AppsV1().ReplicaSets(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["replicasets"] = len(l.Items)
	}
	if l, err := lab.cs.CoreV1().Services(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["services"] = len(l.Items)
	}
	if l, err := lab.cs.CoreV1().Secrets(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["secrets"] = len(l.Items)
	}
	if l, err := lab.cs.NetworkingV1().Ingresses(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["ingresses"] = len(l.Items)
	}
	if l, err := lab.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(routeNamespace).List(lab.ctx, opts); err == nil {
		counts["cilium policies"] = len(l.Items)
	}
	if len(counts) != 7 {
		t.Fatalf("could not list every kind: %v", counts)
	}
	for kind, n := range counts {
		if n != 0 {
			t.Errorf("%d %s left for %s", n, kind, selector)
		}
	}
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
	workload, err := RenderAppWorkload(routeNamespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: liveRoute, DeployID: liveDeployID})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := lab.client.ApplyAppWorkload(lab.ctx, routeNamespace, workload); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := lab.client.WaitForAppRollout(lab.ctx, routeNamespace, AppObjectName(app.Name), liveDeployID, 5*time.Minute); err != nil {
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

// listAppPods lists the route app's live pods, failing the test on any list error.
func (lab *egressLab) listAppPods(t *testing.T) []corev1.Pod {
	t.Helper()
	pods, err := lab.cs.CoreV1().Pods(routeNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: routeAppLabel})
	if err != nil {
		t.Fatalf("list app pods: %v", err)
	}
	return pods.Items
}

func (lab *egressLab) appPodNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, pod := range lab.listAppPods(t) {
		if pod.DeletionTimestamp == nil {
			names[pod.Name] = true
		}
	}
	return names
}

// routeSecondApp carries a second app's identity labels without its egress
// fence, so only the first app's ingress side decides whether it connects.
func routeSecondApp() map[string]string {
	second := &apphost.App{ID: "app-route-2", ProjectID: "proj-routea", Name: "api", Tier: domain.Free}
	return appLabels(second)
}

// routeDatabaseLabels are the labels CNPG puts on the project's database pod.
var routeDatabaseLabels = map[string]string{"cnpg.io/cluster": "proj-routea-postgres", "cnpg.io/podRole": "instance"}

func (lab *egressLab) startFencePods(t *testing.T) {
	t.Helper()
	sleep := []string{"/bin/sleep", "3600"}
	runPod(lab.ctx, t, lab.cs, routeOtherTenant, podIntruder, nil, sleep)
	runPod(lab.ctx, t, lab.cs, routeNamespace, podNeighbour, nil, sleep)
	runPod(lab.ctx, t, lab.cs, routeNamespace, podSecondApp, routeSecondApp(), sleep)
	runPod(lab.ctx, t, lab.cs, routePlatform, podPlatform, nil, sleep)
	runPod(lab.ctx, t, lab.cs, routeNamespace, podDatabase, routeDatabaseLabels,
		[]string{"/agnhost", "netexec", "--http-port=" + strconv.Itoa(routeDatabasePort)})
}

// checkIngressFence proves each refusal is the policies' doing: the same
// clients reach the project's database, and a neighbour connects to the app
// once every policy selecting it is gone.
func (lab *egressLab) checkIngressFence(t *testing.T, app *apphost.App, route *routeProbe) {
	t.Helper()
	lab.startFencePods(t)
	appAddr := net.JoinHostPort(lab.newestAppPodIP(t), strconv.Itoa(app.Port))
	dbAddr := net.JoinHostPort(podIP(lab.ctx, t, lab.cs, routeNamespace, podDatabase), strconv.Itoa(routeDatabasePort))
	connect := func(namespace, pod, target string) error {
		_, err := lab.client.ExecInPod(lab.ctx, namespace, pod, "main", []string{"/agnhost", "connect", "--timeout=3s", target})
		return err
	}
	refused := func(desc, namespace, pod string) {
		eventually(t, desc, time.Minute, func() bool { return connect(namespace, pod, appAddr) != nil })
		for range 3 {
			if connect(namespace, pod, appAddr) == nil {
				t.Errorf("%s: a connection got through", desc)
			}
		}
	}

	if err := route.get(); err != nil {
		t.Fatalf("the edge must reach the app: %v", err)
	}
	refused("another tenant is refused", routeOtherTenant, podIntruder)
	refused("a pod in the app's own namespace is refused", routeNamespace, podNeighbour)
	refused("a second app in the same project is refused", routeNamespace, podSecondApp)
	refused("the project's database is refused", routeNamespace, podDatabase)
	refused("the platform is refused", routePlatform, podPlatform)

	eventually(t, "the project's own pods still reach its database", time.Minute, func() bool { return connect(routeNamespace, podNeighbour, dbAddr) == nil })
	eventually(t, "the platform still reaches the project's database", time.Minute, func() bool { return connect(routePlatform, podPlatform, dbAddr) == nil })
	eventually(t, "an app still reaches its project's database", time.Minute, func() bool { return connect(routeNamespace, podSecondApp, dbAddr) == nil })
	lab.expectAppPodsReady(t)

	policies := lab.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(routeNamespace)
	if err := policies.Delete(lab.ctx, AppIngressPolicyName(app.Name), metav1.DeleteOptions{}); err != nil {
		t.Fatalf("remove the app's ingress policy: %v", err)
	}
	eventually(t, "without its own fence the app is closed, even to the edge", time.Minute, func() bool { return route.get() != nil })
	refused("without its own fence a neighbour is still refused", routeNamespace, podNeighbour)

	if err := lab.cs.NetworkingV1().NetworkPolicies(routeNamespace).Delete(lab.ctx, namespaceDefaultDenyPolicy, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("remove the namespace default deny for the control: %v", err)
	}
	eventually(t, "control: a neighbour connects once no policy selects the app", time.Minute, func() bool { return connect(routeNamespace, podNeighbour, appAddr) == nil })
}

func (lab *egressLab) expectAppPodsReady(t *testing.T) {
	t.Helper()
	pods := lab.listAppPods(t)
	if len(pods) == 0 {
		t.Fatal("list app pods: none found")
	}
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
				t.Errorf("app pod %s is not ready: the kubelet's probes must still pass", pod.Name)
			}
		}
	}
}

func (lab *egressLab) newestAppPodIP(t *testing.T) string {
	t.Helper()
	pods := lab.listAppPods(t)
	if len(pods) == 0 {
		t.Fatal("list app pods: none found")
	}
	for _, pod := range pods {
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

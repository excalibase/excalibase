//go:build live

package k8s

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	netProject      = "org1-proj-neta"
	netOtherProject = "org2-proj-netb"
	netEdge         = "edge-controller"
	netPlatform     = "excalibase-platform"
	netAppImage     = "nginxinc/nginx-unprivileged:1.27"
	netAppPort      = 8080
	netDBPort       = 5432
)

var netRoute = AppRouteOptions{Domain: "apps.test", IngressClass: "haproxy", IngressFromNamespace: netEdge}

func netApp(id, projectID, name string) *apphost.App {
	return &apphost.App{ID: id, ProjectID: projectID, Name: name, Image: netAppImage,
		Port: netAppPort, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumAppPrivateNetwork -v -count=1 -timeout 40m
func TestK3sCiliumAppPrivateNetwork(t *testing.T) {
	lab := &egressLab{ctx: context.Background()}
	lab.startCiliumClusterWith(t, testcontainers.WithFiles(gvisorFiles(t, gvisorPlatformSystrap)...))
	lab.createGVisorRuntimeClass(t)
	t.Setenv("POD_NAMESPACE", netPlatform)
	for _, ns := range []string{netEdge, netPlatform} {
		if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	for ns, org := range map[string]string{netProject: "org1", netOtherProject: "org2"} {
		if err := lab.client.CreateProjectNamespace(lab.ctx, ns, org); err != nil {
			t.Fatalf("create project namespace %s: %v", ns, err)
		}
	}
	web := netApp("app-net-web", "proj-neta", "web")
	api := netApp("app-net-api", "proj-neta", "api")
	foreign := netApp("app-net-foreign", "proj-netb", "api")
	lab.deployNetApp(t, netProject, web)
	lab.deployNetApp(t, netProject, api)
	lab.deployNetApp(t, netOtherProject, foreign)
	lab.startNetFencePods(t)

	fromWeb := func(host string, port int) error { return lab.dialFromApp(netProject, web, host, port) }
	fromAPI := func(host string, port int) error { return lab.dialFromApp(netProject, api, host, port) }
	apiPod := net.JoinHostPort(lab.appPodIP(t, netProject, api), strconv.Itoa(netAppPort))
	foreignHost := "api." + netOtherProject + ".svc.cluster.local"

	t.Run("off by default: apps in one project cannot reach each other", func(t *testing.T) {
		refusedFor(t, "web to api by name", func() error { return fromWeb("api", appServicePort) })
		refusedFor(t, "api to web by name", func() error { return fromAPI("web", appServicePort) })
	})

	for _, ns := range []string{netProject, netOtherProject} {
		if err := lab.client.SetAppPrivateNetwork(lab.ctx, ns, true); err != nil {
			t.Fatalf("turn the private network on in %s: %v", ns, err)
		}
	}
	t.Run("on: apps reach each other by name on the HTTP port", func(t *testing.T) {
		eventually(t, "web reaches http://api", time.Minute, func() bool { return fromWeb("api", appServicePort) == nil })
		eventually(t, "api reaches http://web", time.Minute, func() bool { return fromAPI("web", appServicePort) == nil })
	})
	t.Run("on: nothing else gains access to the apps", func(t *testing.T) {
		refusedFor(t, "an app in another project, both switched on", func() error { return fromWeb(foreignHost, appServicePort) })
		for _, probe := range []struct{ desc, ns, pod string }{
			{"another project's pod", netOtherProject, podIntruder},
			{"a non-app pod in the same project", netProject, podNeighbour},
			{"the project's database pod", netProject, podDatabase},
			{"the platform", netPlatform, podPlatform},
		} {
			refusedFor(t, probe.desc, func() error { return lab.agnhostConnect(probe.ns, probe.pod, apiPod) })
		}
	})
	t.Run("on: the database still serves its project and the apps are still ready", func(t *testing.T) {
		dbAddr := net.JoinHostPort(podIP(lab.ctx, t, lab.cs, netProject, podDatabase), strconv.Itoa(netDBPort))
		eventually(t, "a project pod reaches the database", time.Minute, func() bool {
			return lab.agnhostConnect(netProject, podNeighbour, dbAddr) == nil
		})
		lab.expectNetAppsReady(t, netProject)
	})

	if err := lab.client.SetAppPrivateNetwork(lab.ctx, netProject, false); err != nil {
		t.Fatalf("turn the private network off: %v", err)
	}
	t.Run("off again: the apps are closed to each other", func(t *testing.T) {
		refusedFor(t, "web to api by name", func() error { return fromWeb("api", appServicePort) })
	})
}

func (lab *egressLab) deployNetApp(t *testing.T, namespace string, app *apphost.App) {
	t.Helper()
	workload, err := RenderAppWorkload(namespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: netRoute, DeployID: liveDeployID})
	if err != nil {
		t.Fatalf("render %s: %v", app.Name, err)
	}
	if err := lab.client.ApplyAppWorkload(lab.ctx, namespace, workload); err != nil {
		t.Fatalf("apply %s: %v", app.Name, err)
	}
	if err := lab.client.WaitForAppRollout(lab.ctx, namespace, AppObjectName(app.Name), liveDeployID, 5*time.Minute); err != nil {
		t.Fatalf("rollout %s: %v", app.Name, err)
	}
}

func (lab *egressLab) startNetFencePods(t *testing.T) {
	t.Helper()
	sleep := []string{"/bin/sleep", "3600"}
	runPod(lab.ctx, t, lab.cs, netOtherProject, podIntruder, nil, sleep)
	runPod(lab.ctx, t, lab.cs, netProject, podNeighbour, nil, sleep)
	runPod(lab.ctx, t, lab.cs, netPlatform, podPlatform, nil, sleep)
	runPod(lab.ctx, t, lab.cs, netProject, podDatabase,
		map[string]string{"cnpg.io/cluster": "proj-neta-postgres", "cnpg.io/podRole": "instance"},
		[]string{"/agnhost", "netexec", "--http-port=" + strconv.Itoa(netDBPort)})
}

// dialFromApp opens a TCP connection from inside the app's own container, so
// its egress fence and the target's ingress fence both decide.
func (lab *egressLab) dialFromApp(namespace string, app *apphost.App, host string, port int) error {
	pod, err := lab.firstAppPod(namespace, app)
	if err != nil {
		return err
	}
	script := fmt.Sprintf("exec 3<>/dev/tcp/%s/%d", host, port)
	_, err = lab.client.ExecInPod(lab.ctx, namespace, pod, app.Name, []string{"timeout", "4", "bash", "-c", script})
	return err
}

func (lab *egressLab) agnhostConnect(namespace, pod, target string) error {
	_, err := lab.client.ExecInPod(lab.ctx, namespace, pod, "main", []string{"/agnhost", "connect", "--timeout=3s", target})
	return err
}

func (lab *egressLab) firstAppPod(namespace string, app *apphost.App) (string, error) {
	pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/app=" + app.ID})
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running pod for %s", app.Name)
}

func (lab *egressLab) appPodIP(t *testing.T, namespace string, app *apphost.App) string {
	t.Helper()
	name, err := lab.firstAppPod(namespace, app)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return podIP(lab.ctx, t, lab.cs, namespace, name)
}

func (lab *egressLab) expectNetAppsReady(t *testing.T, namespace string) {
	t.Helper()
	pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/component=app"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("list app pods: %v (%d)", err, len(pods.Items))
	}
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
				t.Errorf("app pod %s is not ready", pod.Name)
			}
		}
	}
}

// refusedFor waits for the fence to hold, then insists it keeps holding.
func refusedFor(t *testing.T, desc string, dial func() error) {
	t.Helper()
	eventually(t, desc+" is refused", time.Minute, func() bool { return dial() != nil })
	for range 3 {
		if err := dial(); err == nil {
			t.Errorf("%s: a connection got through", desc)
		} else if strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: the probe itself failed, not the fence: %v", desc, err)
		}
	}
}

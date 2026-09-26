//go:build live

package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"github.com/testcontainers/testcontainers-go/wait"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const (
	appLiveK3s          = "rancher/k3s:v1.31.6-k3s1"
	appLiveCiliumRepo   = "https://helm.cilium.io"
	appLiveCiliumChart  = "1.18.14"
	appLiveHAProxyRepo  = "https://haproxytech.github.io/helm-charts"
	appLiveHAProxyChart = "1.54.2"
	appLiveHAProxyNS    = "haproxy-controller"
	appLiveNodePort     = 30080
	// The sandbox is proven by the gVisor live tests; here any runtime class the node can run will do.
	appLiveRuntimeClass = "live-runc"
	appLiveGoodImage    = "nginxinc/nginx-unprivileged:1.27"
	appLiveBadImage     = "nginxinc/nginx-unprivileged:0.0.0-never-published"
)

var appLiveRoute = k8s.AppRouteOptions{Domain: "apps.test", IngressClass: "haproxy", IngressFromNamespace: appLiveHAProxyNS}

type appLiveLab struct {
	ctx       context.Context
	container *k3s.K3sContainer
	cs        kubernetes.Interface
	client    *k8s.Client
	nodeIP    string
	instances *fakestore.Instances
}

// Run with: go test ./internal/service/ -tags=live -run TestLiveAppStatusFollowsTheRollout -v -count=1 -timeout 40m
func TestLiveAppStatusFollowsTheRollout(t *testing.T) {
	lab := startAppLiveLab(t)

	t.Run("a first deploy that rolls out makes the app active, and a failed redeploy leaves it serving", func(t *testing.T) {
		app := lab.app(t, "proj-livea", "web", appLiveGoodImage)
		svc, deploys, apps := lab.service(app)
		first := lab.deploy(t, svc, app)
		lab.expectOutcome(t, deploys, first.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
		lab.expectEdge(t, app, http.StatusOK)
		before := lab.readyPods(t, app)

		apps.mu.Lock()
		apps.apps[app.ProjectID+"/"+app.ID].Image = appLiveBadImage
		apps.mu.Unlock()
		redeploy := lab.deploy(t, svc, app)
		failed := lab.expectOutcome(t, deploys, redeploy.ID, apphost.DeployStatusFailed, apphost.StatusRunning)
		t.Logf("redeploy failed with %q; app stays %s", failed.FailureReason, apphost.StatusRunning)
		lab.expectEdge(t, app, http.StatusOK)
		if after := lab.readyPods(t, app); after != before {
			t.Fatalf("the previous version's pod changed: before %q, after %q", before, after)
		}
		t.Logf("previous pod %s still serves through the edge", before)
	})

	t.Run("a first deploy that never runs fails the app", func(t *testing.T) {
		app := lab.app(t, "proj-liveb", "broken", appLiveBadImage)
		svc, deploys, _ := lab.service(app)
		deploy := lab.deploy(t, svc, app)
		failed := lab.expectOutcome(t, deploys, deploy.ID, apphost.DeployStatusFailed, apphost.StatusFailed)
		t.Logf("first deploy failed with %q; app %s", failed.FailureReason, apphost.StatusFailed)
	})

	t.Run("a rollout whose watch died is resolved by another instance's sweep", func(t *testing.T) {
		app := lab.app(t, "proj-livec", "resumed", appLiveGoodImage)
		crashed, deploys, apps := lab.service(app)
		crashed.async = func(func()) {} // the process ends before its watch runs
		deploy := lab.deploy(t, crashed, app)
		if got := deploys.getByID(deploy.ID).Status; got != apphost.DeployStatusRolling {
			t.Fatalf("deploy left behind: got %q want rolling", got)
		}

		restarted := NewAppDeployService(apps, deploys, lab.client, lab.instances, nil, lab.render())
		stop := restarted.StartRolloutSweeper(lab.ctx, NewLeadership(AlwaysLeader{}), time.Minute)
		defer stop()
		lab.expectOutcome(t, deploys, deploy.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
		lab.expectEdge(t, app, http.StatusOK)
	})
}

func startAppLiveLab(t *testing.T) *appLiveLab {
	t.Helper()
	lab := &appLiveLab{ctx: context.Background(), instances: fakestore.NewInstances()}
	container, err := k3s.Run(lab.ctx, appLiveK3s,
		testcontainers.WithCmdArgs("--flannel-backend=none", "--disable-network-policy", "--disable=traefik"),
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForLog("k3s is up and running").WithStartupTimeout(3*time.Minute)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	lab.container = container
	// Cilium's pods mount /sys/fs/bpf with propagation, which a docker container's private root refuses.
	if code, _, err := container.Exec(lab.ctx, []string{"mount", "--make-rshared", "/"}); err != nil || code != 0 {
		t.Fatalf("make the k3s root mount shared: exit %d: %v", code, err)
	}
	lab.connect(t)
	lab.installCilium(t)
	lab.installHAProxy(t)
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: appLiveRuntimeClass}, Handler: "runc"}
	if _, err := lab.cs.NodeV1().RuntimeClasses().Create(lab.ctx, runtimeClass, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create runtime class: %v", err)
	}
	return lab
}

func (lab *appLiveLab) connect(t *testing.T) {
	t.Helper()
	kubeconfig, err := lab.container.GetKubeConfig(lab.ctx)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, kubeconfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	// The Helm SDK reads its cluster from KUBECONFIG.
	t.Setenv("KUBECONFIG", path)
	if lab.client, err = k8s.NewClientWith(k8s.ClientOptions{KubeconfigPath: path}); err != nil {
		t.Fatalf("client: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	if lab.cs, err = kubernetes.NewForConfig(restCfg); err != nil {
		t.Fatalf("clientset: %v", err)
	}
	if lab.nodeIP, err = lab.container.ContainerIP(lab.ctx); err != nil {
		t.Fatalf("node IP: %v", err)
	}
}

func (lab *appLiveLab) installChart(t *testing.T, repo, name, version, namespace, release string, values map[string]interface{}) {
	t.Helper()
	locate := action.ChartPathOptions{RepoURL: repo, Version: version}
	chartPath, err := locate.LocateChart(name, cli.New())
	if err != nil {
		t.Fatalf("fetch %s chart %s: %v", name, version, err)
	}
	if err := lab.client.InstallHelmChart(lab.ctx, namespace, release, chartPath, values); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
}

func (lab *appLiveLab) installCilium(t *testing.T) {
	t.Helper()
	lab.installChart(t, appLiveCiliumRepo, "cilium", appLiveCiliumChart, "kube-system", "cilium", map[string]interface{}{
		"ipam":           map[string]interface{}{"mode": "kubernetes"},
		"k8sServiceHost": lab.nodeIP,
		"k8sServicePort": "6443",
		"operator":       map[string]interface{}{"replicas": 1},
	})
	// Pods only get addresses once CoreDNS, scheduled before any CNI existed, is running on Cilium.
	eventuallyLive(t, "CoreDNS ready on Cilium", 5*time.Minute, func() bool {
		coredns, err := lab.cs.AppsV1().Deployments("kube-system").Get(lab.ctx, "coredns", metav1.GetOptions{})
		return err == nil && coredns.Status.AvailableReplicas > 0
	})
}

func (lab *appLiveLab) installHAProxy(t *testing.T) {
	t.Helper()
	lab.namespace(t, appLiveHAProxyNS)
	lab.installChart(t, appLiveHAProxyRepo, "kubernetes-ingress", appLiveHAProxyChart, appLiveHAProxyNS, "haproxy", map[string]interface{}{
		"controller": map[string]interface{}{
			"replicaCount":         1,
			"ingressClass":         appLiveRoute.IngressClass,
			"ingressClassResource": map[string]interface{}{"name": appLiveRoute.IngressClass},
			"service":              map[string]interface{}{"type": "NodePort", "nodePorts": map[string]interface{}{"http": appLiveNodePort}},
		},
	})
}

func (lab *appLiveLab) namespace(t *testing.T, name string) {
	t.Helper()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, namespace, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

func (lab *appLiveLab) render() k8s.AppRenderOptions {
	return k8s.AppRenderOptions{RuntimeClass: appLiveRuntimeClass, Route: appLiveRoute}
}

// app gives each case a project and namespace of its own.
func (lab *appLiveLab) app(t *testing.T, projectID, name, image string) *apphost.App {
	t.Helper()
	namespace := "org1-" + projectID
	lab.namespace(t, namespace)
	lab.instances.Items[projectID] = &domain.DatabaseInstance{ProjectID: projectID, Namespace: namespace}
	return &apphost.App{ID: "app-" + name, ProjectID: projectID, Name: name, Image: image,
		Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
}

func (lab *appLiveLab) service(app *apphost.App) (*AppDeployService, *fakeDeployStore, *fakeAppStoreForDeploy) {
	apps := newFakeAppStoreForDeploy(app)
	deploys := newFakeDeployStore()
	return NewAppDeployService(apps, deploys, lab.client, lab.instances, nil, lab.render()), deploys, apps
}

func (lab *appLiveLab) deploy(t *testing.T, svc *AppDeployService, app *apphost.App) *apphost.Deploy {
	t.Helper()
	deploy, err := svc.DeployApp(lab.ctx, app.ProjectID, app.ID, "live")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	return deploy
}

func (lab *appLiveLab) expectOutcome(t *testing.T, deploys *fakeDeployStore, deployID, deployStatus, appStatus string) *apphost.Deploy {
	t.Helper()
	var deploy *apphost.Deploy
	eventuallyLive(t, "deploy "+deployID+" "+deployStatus, 7*time.Minute, func() bool {
		deploy = deploys.getByID(deployID)
		return deploy.Status != apphost.DeployStatusPending && deploy.Status != apphost.DeployStatusRolling
	})
	if deploy.Status != deployStatus {
		t.Fatalf("deploy: got %q (%s), want %q", deploy.Status, deploy.FailureReason, deployStatus)
	}
	if got := deploys.appStatusOf(deploy.AppID); got != appStatus {
		t.Fatalf("app status: got %q, want %q", got, appStatus)
	}
	t.Logf("deploy %s; app %s", deploy.Status, appStatus)
	return deploy
}

func (lab *appLiveLab) expectEdge(t *testing.T, app *apphost.App, want int) {
	t.Helper()
	host, err := appLiveRoute.Public().Hostname(app.Name, app.ProjectID)
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	url := "http://" + net.JoinHostPort(lab.nodeIP, strconv.Itoa(appLiveNodePort)) + "/"
	client := &http.Client{Timeout: 5 * time.Second}
	var last string
	eventuallyLive(t, "the app answers through the edge", 3*time.Minute, func() bool {
		request, _ := http.NewRequest(http.MethodGet, url, nil)
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			last = err.Error()
			return false
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		last = fmt.Sprintf("status %d", response.StatusCode)
		return response.StatusCode == want
	})
	t.Logf("edge %s: %s", host, last)
}

// readyPods names the app's ready pods, so a test can see which version serves.
func (lab *appLiveLab) readyPods(t *testing.T, app *apphost.App) string {
	t.Helper()
	pods, err := lab.cs.CoreV1().Pods("org1-"+app.ProjectID).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/app=" + app.ID})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	ready := ""
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready += pod.Name + " "
			}
		}
	}
	return ready
}

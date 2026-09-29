//go:build live

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/gvisortest"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

const (
	tplLiveProject      = "proj-tmpl"
	tplLiveNamespace    = "org1-" + tplLiveProject
	tplLiveRuntimeClass = "gvisor"
	// busybox 1.37, pinned the way APP_DISK_TOOLS_IMAGE must be.
	tplLiveDiskTools = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
)

// A template of three apps: FREE holds two.
const threeAppTemplate = `
format: excalibase.template/v1
id: three
name: Three apps
summary: More apps than FREE holds.
apps:
  - {name: one, image: "nginxinc/nginx-unprivileged:1.27", port: 8080, replicas: 1}
  - {name: two, image: "nginxinc/nginx-unprivileged:1.27", port: 8080, replicas: 1}
  - {name: three, image: "nginxinc/nginx-unprivileged:1.27", port: 8080, replicas: 1}
`

// failingKube fails the apply of one app's workload, after the apps before it were applied.
type failingKube struct {
	k8s.KubeClient
	failFor string
	mu      sync.Mutex
	applied []string
}

func (f *failingKube) ApplyAppWorkload(ctx context.Context, namespace string, workload *k8s.AppWorkload) error {
	f.mu.Lock()
	f.applied = append(f.applied, workload.Deployment.Name)
	f.mu.Unlock()
	if workload.Deployment.Name == k8s.AppObjectName(f.failFor) {
		return errors.New("forced failure for the rollback proof")
	}
	return f.KubeClient.ApplyAppWorkload(ctx, namespace, workload)
}

type templateLive struct {
	lab     *appLiveLab
	store   *postgres.Store
	apps    *apphost.PostgresAppStore
	deploys *apphost.PostgresDeployStore
	vault   *vault.Vault
	network *AppNetworkService
	logs    *lockedWriter
}

// Run with: go test ./internal/service/ -tags=live -run TestLiveTemplates -v -count=1 -timeout 60m
// k3s + Cilium + HAProxy, apps sandboxed by gVisor (systrap), the real Postgres
// app and deploy stores, and an in-process vault.
func TestLiveTemplates(t *testing.T) {
	live := startTemplateLive(t)

	t.Run("a template over the plan is refused before anything is created", func(t *testing.T) {
		catalog, err := apptemplate.NewCatalog(mustParseTemplate(t, threeAppTemplate))
		if err != nil {
			t.Fatal(err)
		}
		_, err = live.service(catalog, live.lab.client).Deploy(live.lab.ctx, tplLiveProject, "three", "live", adminConfirmed)
		var refused *TemplateRefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "needs 3 apps") {
			t.Fatalf("want a refusal naming the plan, got %v", err)
		}
		t.Logf("refused: %v", err)
		live.expectNothingLeft(t)
	})

	t.Run("a failure mid-way rolls everything back", func(t *testing.T) {
		kube := &failingKube{KubeClient: live.lab.client, failFor: "web"}
		_, err := live.service(live.builtins(t), kube).Deploy(live.lab.ctx, tplLiveProject, "web-redis", "live", adminConfirmed)
		var failed *TemplateFailedError
		if !errors.As(err, &failed) || !strings.Contains(err.Error(), "forced failure") {
			t.Fatalf("want a rolled-back failure, got %v", err)
		}
		if len(kube.applied) != 2 || kube.applied[0] != k8s.AppObjectName("redis") {
			t.Fatalf("redis must have been applied before web failed: %v", kube.applied)
		}
		t.Logf("rolled back: %v", err)
		live.expectNothingLeft(t)
	})

	var password string
	t.Run("web app + Redis: both run, web reaches redis by name with the generated password", func(t *testing.T) {
		result, err := live.service(live.builtins(t), live.lab.client).Deploy(live.lab.ctx, tplLiveProject, "web-redis", "live", adminConfirmed)
		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if !result.PrivateNetworkTurnedOn || len(result.Apps) != 2 {
			t.Fatalf("result %+v", result)
		}
		for _, app := range result.Apps {
			live.expectDeploySucceeded(t, app)
		}
		redis, web := live.appByName(t, "redis"), live.appByName(t, "web")
		password = live.secret(t, redis, "REDIS_PASSWORD")
		if len(password) != 32 || live.secret(t, web, "REDIS_PASSWORD") != password {
			t.Fatal("web must hold redis's generated password")
		}
		live.expectSandboxed(t, redis)
		live.expectSandboxed(t, web)

		reply := live.redisFromWeb(t, web, `printf 'AUTH %s\r\nPING\r\n' "$REDIS_PASSWORD" >&3; read -t 3 a <&3; read -t 3 b <&3; printf '%s %s' "$a" "$b"`)
		if !strings.HasPrefix(reply, "+OK") || !strings.Contains(reply, "+PONG") {
			t.Fatalf("web to redis:6379 with the generated password: %q", reply)
		}
		t.Logf("web -> $REDIS_HOST:$REDIS_PORT (redis:6379): AUTH + PING = %q", reply)
		noAuth := live.redisFromWeb(t, web, `printf 'PING\r\n' >&3; read -t 3 a <&3; printf '%s' "$a"`)
		if !strings.HasPrefix(noAuth, "-NOAUTH") {
			t.Fatalf("redis must refuse a client without the password: %q", noAuth)
		}
		wrong := live.redisFromWeb(t, web, `printf 'AUTH not-the-password\r\n' >&3; read -t 3 a <&3; printf '%s' "$a"`)
		if !strings.HasPrefix(wrong, "-WRONGPASS") {
			t.Fatalf("redis must refuse a wrong password: %q", wrong)
		}
		t.Logf("without the password: %q; with a wrong one: %q", noAuth, wrong)
		live.lab.expectEdge(t, web, 200)
		result.Apps = nil
		blob, _ := json.Marshal(result)
		if strings.Contains(string(blob), password) {
			t.Fatal("the deploy result carries the password")
		}
	})

	t.Run("the generated password is in no Deployment, pod spec or log", func(t *testing.T) {
		if password == "" {
			t.Skip("the deploy did not run")
		}
		deployments, err := live.lab.cs.AppsV1().Deployments(tplLiveNamespace).List(live.lab.ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pods, err := live.lab.cs.CoreV1().Pods(tplLiveNamespace).List(live.lab.ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		specs, _ := json.Marshal([]any{deployments, pods})
		if strings.Contains(string(specs), password) {
			t.Fatal("a Deployment or pod spec carries the password")
		}
		for _, pod := range pods.Items {
			raw, err := live.lab.cs.CoreV1().Pods(tplLiveNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(live.lab.ctx)
			if err != nil {
				t.Fatalf("logs of %s: %v", pod.Name, err)
			}
			if strings.Contains(string(raw), password) {
				t.Fatalf("pod %s logs the password", pod.Name)
			}
		}
		controlPlane := live.logs.String()
		if strings.Contains(controlPlane, password) {
			t.Fatal("the control plane logged the password")
		}
		for _, deployment := range deployments.Items {
			t.Logf("%s: args %v", deployment.Name, deployment.Spec.Template.Spec.Containers[0].Args)
		}
		t.Logf("checked %d Deployments, %d pods and their logs, and %d bytes of control-plane log", len(deployments.Items), len(pods.Items), len(controlPlane))
	})
}

func startTemplateLive(t *testing.T) *templateLive {
	t.Helper()
	logs := &lockedWriter{w: &bytes.Buffer{}}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	lab := startAppLiveLabWith(t, tplLiveRuntimeClass, gvisortest.Handler,
		testcontainers.WithFiles(gvisortest.Files(t, "systrap")...))
	// Provisioning's tenant role is the chart's; here cluster-admin stands in for it.
	access := k8s.ProjectAccess{ClusterRole: "cluster-admin", ServiceAccount: "provisioning", Namespace: "excalibase-platform"}
	if err := lab.client.WithProjectAccess(access).CreateProjectNamespace(lab.ctx, tplLiveNamespace, "org1"); err != nil {
		t.Fatalf("create project namespace: %v", err)
	}
	lab.instances.Items[tplLiveProject] = &domain.DatabaseInstance{ProjectID: tplLiveProject, Namespace: tplLiveNamespace,
		OrgID: "org1", DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive), NoDatabase: true}

	store := pgstore.New(t)
	secrets, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Init(1, 1); err != nil {
		t.Fatal(err)
	}
	return &templateLive{
		lab: lab, store: store, logs: logs, vault: secrets,
		apps:    apphost.NewPostgresAppStore(store.DB()),
		deploys: apphost.NewPostgresDeployStore(store.DB()),
		network: NewAppNetworkService(store, lab.instances, lab.client, nil),
	}
}

// service wires the template service the way the server does, over kube.
func (l *templateLive) service(catalog *apptemplate.Catalog, kube k8s.KubeClient) *AppTemplateService {
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	plans := NewOrgPlanTiers(l.lab.instances, orgs)
	free, _ := config.GetTierConfig(domain.Free)
	tiers := fixedTierConfigs{domain.Free: free}
	render := l.lab.render()
	render.DiskStorageClass = "local-path"
	deployer := NewAppDeployService(l.apps, l.deploys, kube, l.lab.instances, NewAppEnvResolver(l.vault, l.lab.instances), render)
	deployer.SetPlanTiers(plans)
	deployer.SetNamespaceQuotaTiers(tiers)
	disks := NewAppDiskLimits(plans, tiers)
	deployer.SetDiskLimits(disks)
	deployer.SetSecretPurger(l.vault)
	deployer.SetDiskJobs(k8s.DiskJobOptions{Image: tplLiveDiskTools, RuntimeClass: tplLiveRuntimeClass, Timeout: 5 * time.Minute})
	return NewAppTemplateService(catalog, AppTemplateDeps{
		Apps: l.apps, Deployer: deployer, Projects: l.lab.instances, Plans: plans,
		Limits: NewAppLimits(plans, tiers), Disks: disks, Network: l.network, Secrets: l.vault,
	})
}

func (l *templateLive) builtins(t *testing.T) *apptemplate.Catalog {
	t.Helper()
	catalog, err := apptemplate.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func mustParseTemplate(t *testing.T, source string) *apptemplate.Template {
	t.Helper()
	tpl, err := apptemplate.Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

// expectNothingLeft: no app record, workload, claim, app Secret, stored value or open network.
func (l *templateLive) expectNothingLeft(t *testing.T) {
	t.Helper()
	ctx := l.lab.ctx
	apps, err := l.apps.List(tplLiveProject)
	if err != nil || len(apps) != 0 {
		t.Fatalf("app records left: %d (%v)", len(apps), err)
	}
	eventuallyLive(t, "no app object left in "+tplLiveNamespace, 3*time.Minute, func() bool {
		return l.countAppObjects(t) == 0
	})
	paths, err := l.vault.List("projects/" + tplLiveProject + "/")
	if err != nil || len(paths) != 0 {
		t.Fatalf("stored values left: %v (%v)", paths, err)
	}
	open, err := l.lab.client.AppPrivateNetworkOpen(ctx, tplLiveNamespace)
	if err != nil || open {
		t.Fatalf("the private network is open: %v (%v)", open, err)
	}
	recorded, err := l.store.GetAppPrivateNetwork(ctx, tplLiveProject)
	if err != nil || recorded {
		t.Fatalf("the setting says on: %v (%v)", recorded, err)
	}
	t.Logf("nothing left: no app record, Deployment, Service, Ingress, claim, app Secret, stored value; network off")
}

func (l *templateLive) countAppObjects(t *testing.T) int {
	t.Helper()
	ctx, ns := l.lab.ctx, tplLiveNamespace
	app := metav1.ListOptions{LabelSelector: "excalibase.io/component=app"}
	deployments, err1 := l.lab.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	services, err2 := l.lab.cs.CoreV1().Services(ns).List(ctx, app)
	ingresses, err3 := l.lab.cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
	claims, err4 := l.lab.cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	secrets, err5 := l.lab.cs.CoreV1().Secrets(ns).List(ctx, app)
	pods, err6 := l.lab.cs.CoreV1().Pods(ns).List(ctx, app)
	if err := errors.Join(err1, err2, err3, err4, err5, err6); err != nil {
		t.Fatalf("list the namespace: %v", err)
	}
	return len(deployments.Items) + len(services.Items) + len(ingresses.Items) + len(claims.Items) + len(secrets.Items) + len(pods.Items)
}

func (l *templateLive) appByName(t *testing.T, name string) *apphost.App {
	t.Helper()
	apps, err := l.apps.List(tplLiveProject)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.Name == name {
			return app
		}
	}
	t.Fatalf("no app %s", name)
	return nil
}

func (l *templateLive) secret(t *testing.T, app *apphost.App, name string) string {
	t.Helper()
	ref := apphost.AppSecretRef(tplLiveProject, app.ID, name)
	record, err := l.vault.Get(ref.Path)
	if err != nil {
		t.Fatalf("read %s of %s: %v", name, app.Name, err)
	}
	return record[ref.Key]
}

func (l *templateLive) expectDeploySucceeded(t *testing.T, deployed TemplateDeployedApp) {
	t.Helper()
	var deploy *apphost.Deploy
	eventuallyLive(t, deployed.Name+" rolled out", 8*time.Minute, func() bool {
		got, err := l.deploys.Get(tplLiveProject, deployed.ID, deployed.DeployID)
		deploy = got
		return err == nil && got != nil && got.Status != apphost.DeployStatusPending && got.Status != apphost.DeployStatusRolling
	})
	if deploy.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("%s: deploy %s (%s)", deployed.Name, deploy.Status, deploy.FailureReason)
	}
	t.Logf("%s: deploy %s", deployed.Name, deploy.Status)
}

func (l *templateLive) expectSandboxed(t *testing.T, app *apphost.App) {
	t.Helper()
	pod := l.lab.servingPod(t, tplLiveNamespace, app)
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != tplLiveRuntimeClass {
		t.Fatalf("%s runs outside gVisor: %v", app.Name, pod.Spec.RuntimeClassName)
	}
	out, err := l.lab.client.ExecInPod(l.lab.ctx, tplLiveNamespace, pod.Name, app.Name, []string{"sh", "-c", "dmesg 2>&1"})
	if err != nil || !strings.Contains(out, "Starting gVisor") {
		t.Fatalf("%s: no gVisor kernel banner (%v): %q", app.Name, err, out)
	}
	t.Logf("%s runs under gVisor (dmesg shows its boot banner)", app.Name)
}

// redisFromWeb speaks to $REDIS_HOST:$REDIS_PORT from inside web's container, with web's own env.
func (l *templateLive) redisFromWeb(t *testing.T, web *apphost.App, talk string) string {
	t.Helper()
	pod := l.lab.servingPod(t, tplLiveNamespace, web)
	script := `exec 3<>"/dev/tcp/$REDIS_HOST/$REDIS_PORT" || exit 7; ` + talk
	out, err := l.lab.client.ExecInPod(l.lab.ctx, tplLiveNamespace, pod.Name, web.Name, []string{"timeout", "8", "bash", "-c", script})
	if err != nil {
		t.Fatalf("exec in web: %v (%q)", err, out)
	}
	return strings.ReplaceAll(out, "\r", "")
}

// lockedWriter lets the rollout goroutines log while the test reads the buffer.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

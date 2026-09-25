package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const (
	deployTestProject   = "proj-deploy1"
	deployTestApp       = "app_deploy1"
	testDeployNamespace = "ns-proj-deploy1"
)

type fakeAppStoreForDeploy struct {
	mu     sync.Mutex
	apps   map[string]*apphost.App
	getErr error
}

func newFakeAppStoreForDeploy(app *apphost.App) *fakeAppStoreForDeploy {
	return &fakeAppStoreForDeploy{apps: map[string]*apphost.App{app.ProjectID + "/" + app.ID: app}}
}

func (f *fakeAppStoreForDeploy) Create(*apphost.App) error { return errors.New("not used") }

func (f *fakeAppStoreForDeploy) Get(projectID, id string) (*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	app, ok := f.apps[projectID+"/"+id]
	if !ok {
		return nil, nil
	}
	copied := *app
	return &copied, nil
}

func (f *fakeAppStoreForDeploy) List(string) ([]*apphost.App, error) { return nil, nil }
func (f *fakeAppStoreForDeploy) Update(*apphost.App, int) error      { return errors.New("not used") }
func (f *fakeAppStoreForDeploy) Delete(string, string) error         { return errors.New("not used") }

type fakeDeployStore struct {
	mu        sync.Mutex
	deploys   []*apphost.Deploy
	createErr error
	listErr   error
	updateErr error
	getErr    error
}

func newFakeDeployStore() *fakeDeployStore { return &fakeDeployStore{} }

func (f *fakeDeployStore) Create(deploy *apphost.Deploy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	revision := 0
	for _, d := range f.deploys {
		if d.AppID != deploy.AppID {
			continue
		}
		if d.Status == apphost.DeployStatusPending || d.Status == apphost.DeployStatusRolling {
			d.Status = apphost.DeployStatusSuperseded
		}
		if d.Revision > revision {
			revision = d.Revision
		}
	}
	deploy.Revision = revision + 1
	stored := *deploy
	f.deploys = append(f.deploys, &stored)
	return nil
}

func (f *fakeDeployStore) UpdateStatus(id, status, failureReason string, finishedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	for _, d := range f.deploys {
		if d.ID != id {
			continue
		}
		if d.Status != apphost.DeployStatusPending && d.Status != apphost.DeployStatusRolling {
			return nil
		}
		d.Status = status
		d.FailureReason = failureReason
		d.FinishedAt = finishedAt
		return nil
	}
	return nil
}

func (f *fakeDeployStore) ListByApp(projectID, appID string, limit int) ([]*apphost.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*apphost.Deploy, 0)
	for i := len(f.deploys) - 1; i >= 0; i-- {
		d := f.deploys[i]
		if d.ProjectID == projectID && d.AppID == appID {
			copied := *d
			out = append(out, &copied)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeDeployStore) GetLatest(projectID, appID string) (*apphost.Deploy, error) {
	deploys, err := f.ListByApp(projectID, appID, 1)
	if err != nil || len(deploys) == 0 {
		return nil, err
	}
	return deploys[0], nil
}

func (f *fakeDeployStore) Get(projectID, appID, id string) (*apphost.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, d := range f.deploys {
		if d.ID == id && d.AppID == appID && d.ProjectID == projectID {
			copied := *d
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeDeployStore) getByID(id string) *apphost.Deploy {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deploys {
		if d.ID == id {
			copied := *d
			return &copied
		}
	}
	return nil
}

func waitForDeployStatus(t *testing.T, store *fakeDeployStore, id, want string) *apphost.Deploy {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d := store.getByID(id)
		if d != nil && d.Status == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy %s did not reach status %q in time (last: %+v)", id, want, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func sampleDeployApp() *apphost.App {
	secret := "sk_live_should_never_be_stored"
	return &apphost.App{
		ID:        deployTestApp,
		ProjectID: deployTestProject,
		Name:      "storefront",
		Image:     "ghcr.io/acme/storefront:1.4.2",
		Env:       []apphost.EnvVar{{Name: "API_KEY", Kind: apphost.KindLiteral, Value: &secret}},
		Port:      8080,
		Replicas:  1,
		Tier:      domain.Free,
		Status:    apphost.StatusCreated,
	}
}

func newDeployTestService(t *testing.T, app *apphost.App) (*AppDeployService, *fakeDeployStore, *k8s.MockClient) {
	t.Helper()
	appStore := newFakeAppStoreForDeploy(app)
	deployStore := newFakeDeployStore()
	kube := k8s.NewMockClient()
	instances := fakestore.NewInstances()
	instances.Items[app.ProjectID] = &domain.DatabaseInstance{
		ProjectID: app.ProjectID, Namespace: testDeployNamespace,
	}
	svc := NewAppDeployService(appStore, deployStore, kube, instances, nil, testDeployRender)
	svc.async = func(f func()) { f() }
	return svc, deployStore, kube
}

var testDeployRender = k8s.AppRenderOptions{RuntimeClass: "gvisor", Route: k8s.AppRouteOptions{
	Domain: "apps.example.com", IngressClass: "haproxy", TLSSecret: "apps-tls", IngressFromNamespace: "haproxy-controller",
}}

func TestDeployApp_RecordsTheAppURL(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	want := "https://" + app.Name + "-deploy1.apps.example.com"
	if deploy.Spec.URL != want {
		t.Errorf("deploy URL = %q, want %q", deploy.Spec.URL, want)
	}
	if stored := deploys.deploys[0]; stored.Spec.URL != want {
		t.Errorf("stored deploy URL = %q, want %q", stored.Spec.URL, want)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil || workload.Ingress == nil || workload.Ingress.Spec.Rules[0].Host != app.Name+"-deploy1.apps.example.com" {
		t.Fatal("the ingress for the recorded URL should have been applied")
	}
}

func TestDeployApp_FailsWhenTheAppHasNoHostname(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.render.Route.Domain = ""

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed || !strings.Contains(deploy.FailureReason, "app domain") {
		t.Fatalf("want a failed deploy naming the missing domain, got %q: %s", deploy.Status, deploy.FailureReason)
	}
	if len(kube.AppWorkloads) != 0 {
		t.Error("nothing may be applied for an app with no hostname")
	}
}

func rolloutKey(app *apphost.App, namespace string) string {
	return namespace + "/" + k8s.AppObjectName(app.Name)
}

func TestDeployApp_Success(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	namespace := testDeployNamespace

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("status: got %q want %q (reason: %s)", deploy.Status, apphost.DeployStatusSucceeded, deploy.FailureReason)
	}
	if deploy.FinishedAt == nil {
		t.Error("a succeeded deploy must record when it finished")
	}
	if deploy.Revision != 1 {
		t.Errorf("revision: got %d want 1", deploy.Revision)
	}
	if _, ok := kube.AppWorkloads[rolloutKey(app, namespace)]; !ok {
		t.Error("the workload should have been applied")
	}
}

func TestDeployApp_RunsUnderTheConfiguredRuntimeClass(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil {
		t.Fatal("the workload should have been applied")
	}
	if got := workload.Deployment.Spec.Template.Spec.RuntimeClassName; got == nil || *got != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", got)
	}
}

func TestDeployApp_AppliesTheConfiguredExtraDenyRanges(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.render.ExtraDenyCIDRs = []string{"203.0.113.9/32"}

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil || workload.EgressPolicy == nil {
		t.Fatal("the workload and its egress policy should have been applied")
	}
	if !strings.Contains(fmt.Sprint(workload.EgressPolicy.Object["spec"]), "203.0.113.9/32") {
		t.Errorf("APP_EGRESS_EXTRA_DENY_CIDRS must reach the egress policy, got %v", workload.EgressPolicy.Object["spec"])
	}
}

func TestDeployApp_ImagePullFailure(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	namespace := testDeployNamespace
	kube.AppRolloutErr[rolloutKey(app, namespace)] = fmt.Errorf("%w: storefront ImagePullBackOff: back-off pulling image", k8s.ErrAppRollout)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", deploy.Status)
	}
	if !strings.Contains(deploy.FailureReason, "ImagePullBackOff") {
		t.Errorf("failure reason should name it: %q", deploy.FailureReason)
	}
}

func TestDeployApp_CrashLoop(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	namespace := testDeployNamespace
	kube.AppRolloutErr[rolloutKey(app, namespace)] = fmt.Errorf("%w: storefront CrashLoopBackOff: back-off restarting failed container", k8s.ErrAppRollout)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", deploy.Status)
	}
	if !strings.Contains(deploy.FailureReason, "CrashLoopBackOff") {
		t.Errorf("failure reason should name it: %q", deploy.FailureReason)
	}
}

func TestDeployApp_Timeout(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	namespace := testDeployNamespace
	kube.AppRolloutErr[rolloutKey(app, namespace)] = fmt.Errorf("%w: storefront did not become ready within 5m0s", k8s.ErrAppRollout)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", deploy.Status)
	}
	if !strings.Contains(deploy.FailureReason, "did not become ready") {
		t.Errorf("failure reason should name the timeout: %q", deploy.FailureReason)
	}
}

func TestDeployApp_EnvValuesNeverStoredInSnapshot(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, _ := newDeployTestService(t, app)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if len(deploy.Spec.Env) != 1 || deploy.Spec.Env[0].Name != "API_KEY" || deploy.Spec.Env[0].Kind != apphost.KindLiteral {
		t.Fatalf("env: got %+v", deploy.Spec.Env)
	}
	blob, err := json.Marshal(deploy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "sk_live_should_never_be_stored") {
		t.Fatal("the deploy record must never carry an env variable's value")
	}

	stored, err := deployStore.GetLatest(app.ProjectID, app.ID)
	if err != nil || stored == nil {
		t.Fatalf("get latest: %v %v", stored, err)
	}
	storedBlob, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	if strings.Contains(string(storedBlob), "sk_live_should_never_be_stored") {
		t.Fatal("the stored deploy record must never carry an env variable's value")
	}
}

func TestDeployApp_SupersedesEarlierPendingOrRolling(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, kube := newDeployTestService(t, app)
	svc.async = func(f func()) { go f() } // both rollout waits run off this goroutine

	var calls int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondReturned := make(chan struct{})
	kube.AppRolloutFunc = func(ctx context.Context, ns, name string, timeout time.Duration) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		defer close(secondReturned)
		return nil
	}

	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("first DeployApp: %v", err)
	}
	if first.Status != apphost.DeployStatusRolling {
		t.Fatalf("first deploy status: got %q want rolling", first.Status)
	}
	<-firstStarted

	second, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-2")
	if err != nil {
		t.Fatalf("second DeployApp: %v", err)
	}
	if second.Revision != first.Revision+1 {
		t.Errorf("revision: got %d want %d", second.Revision, first.Revision+1)
	}

	supersededFirst := deployStore.getByID(first.ID)
	if supersededFirst == nil || supersededFirst.Status != apphost.DeployStatusSuperseded {
		t.Fatalf("first deploy after second was created: got %+v", supersededFirst)
	}

	close(releaseFirst)
	<-secondReturned
	waitForDeployStatus(t, deployStore, second.ID, apphost.DeployStatusSucceeded)

	stillSuperseded := deployStore.getByID(first.ID)
	if stillSuperseded == nil || stillSuperseded.Status != apphost.DeployStatusSuperseded {
		t.Fatalf("first deploy after its late rollout finished: got %+v, want it to stay superseded", stillSuperseded)
	}
}

func TestDeployApp_AppNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)

	_, err := svc.DeployApp(context.Background(), app.ProjectID, "does-not-exist", "dev-1")
	if !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("expected ErrAppNotFound, got %v", err)
	}
}

func TestListDeploys_CrossProjectIsNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}

	_, err := svc.ListDeploys("some-other-project", app.ID, 0)
	if !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("expected ErrAppNotFound for a cross-project app id, got %v", err)
	}

	deploys, err := svc.ListDeploys(app.ProjectID, app.ID, 0)
	if err != nil {
		t.Fatalf("ListDeploys: %v", err)
	}
	if len(deploys) != 1 {
		t.Fatalf("expected 1 deploy, got %d", len(deploys))
	}
}

func TestNewAppDeployService_Defaults(t *testing.T) {
	app := sampleDeployApp()
	appStore := newFakeAppStoreForDeploy(app)
	deployStore := newFakeDeployStore()
	kube := k8s.NewMockClient()
	instances := fakestore.NewInstances()

	svc := NewAppDeployService(appStore, deployStore, kube, instances, nil, k8s.AppRenderOptions{RuntimeClass: "gvisor"})

	if svc.timeout != defaultAppRolloutTimeout {
		t.Errorf("timeout: got %s want %s", svc.timeout, defaultAppRolloutTimeout)
	}
	if svc.active == nil {
		t.Error("active map must be initialized")
	}
	if svc.async == nil {
		t.Fatal("async must be initialized")
	}
	done := make(chan struct{})
	svc.async(func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the default async runner did not run the function in a goroutine")
	}
}

func TestDeployApp_AppLookupError(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.apps.(*fakeAppStoreForDeploy).getErr = errors.New("db down")

	_, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err == nil || !strings.Contains(err.Error(), "look up app") {
		t.Fatalf("expected a wrapped lookup error, got %v", err)
	}
}

func TestDeployApp_InvalidTier(t *testing.T) {
	app := sampleDeployApp()
	app.Tier = "not-a-real-tier"
	svc, _, _ := newDeployTestService(t, app)

	_, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err == nil || !strings.Contains(err.Error(), "resolve app tier") {
		t.Fatalf("expected a wrapped tier error, got %v", err)
	}
}

func TestDeployApp_CreateError(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, _ := newDeployTestService(t, app)
	deployStore.createErr = errors.New("db down")

	_, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("expected the store's error, got %v", err)
	}
}

func TestDeployApp_NamespaceMissingMarksDeployFailed(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	instances := fakestore.NewInstances() // holds no instance for app.ProjectID
	svc.instances = instances

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", deploy.Status)
	}
	if !strings.Contains(deploy.FailureReason, "namespace") {
		t.Errorf("failure reason should name it: %q", deploy.FailureReason)
	}
}

func TestDeployApp_ApplyWorkloadErrorMarksDeployFailed(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	kube.ApplyAppWorkloadErr = errors.New("apply refused")

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", deploy.Status)
	}
	if !strings.Contains(deploy.FailureReason, "apply refused") {
		t.Errorf("failure reason should name it: %q", deploy.FailureReason)
	}
}

func TestListDeploys_AppLookupError(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.apps.(*fakeAppStoreForDeploy).getErr = errors.New("db down")

	_, err := svc.ListDeploys(app.ProjectID, app.ID, 0)
	if err == nil || !strings.Contains(err.Error(), "look up app") {
		t.Fatalf("expected a wrapped lookup error, got %v", err)
	}
}

func TestListDeploys_StoreError(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, _ := newDeployTestService(t, app)
	deployStore.listErr = errors.New("db down")

	_, err := svc.ListDeploys(app.ProjectID, app.ID, 0)
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("expected the store's error, got %v", err)
	}
}

func TestNamespaceFor_InstanceLookupError(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	instances := fakestore.NewInstances()
	instances.Err = errors.New("db down")
	svc.instances = instances

	_, err := svc.namespaceFor(app.ProjectID)
	if err == nil || !strings.Contains(err.Error(), "look up project namespace") {
		t.Fatalf("expected a wrapped lookup error, got %v", err)
	}
}

func TestNamespaceFor_NoInstanceIsAnError(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.instances = fakestore.NewInstances()

	_, err := svc.namespaceFor(app.ProjectID)
	if err == nil {
		t.Fatal("expected an error for a project with no instance")
	}
}

func TestRedeployApp_UsesFrozenConfigNotCurrentApp(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)

	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	frozenImage := first.Image
	if frozenImage != app.Image {
		t.Fatalf("first deploy image: got %q want %q", frozenImage, app.Image)
	}

	// The app record changes after the deploy; a redeploy must ignore this.
	appStore := svc.apps.(*fakeAppStoreForDeploy)
	appStore.mu.Lock()
	appStore.apps[app.ProjectID+"/"+app.ID].Image = "ghcr.io/acme/storefront:9.9.9"
	appStore.apps[app.ProjectID+"/"+app.ID].Env = nil
	appStore.mu.Unlock()

	redeploy, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev-2")
	if err != nil {
		t.Fatalf("RedeployApp: %v", err)
	}
	if redeploy.Image != frozenImage {
		t.Errorf("redeploy image: got %q want the frozen %q, not the app's new image", redeploy.Image, frozenImage)
	}
	if len(redeploy.Spec.Env) != 1 || redeploy.Spec.Env[0].Name != "API_KEY" {
		t.Errorf("redeploy env: got %+v, want the frozen env", redeploy.Spec.Env)
	}
	if redeploy.RedeployOf != first.ID {
		t.Errorf("redeployOf: got %q want %q", redeploy.RedeployOf, first.ID)
	}
	if redeploy.Revision != first.Revision+1 {
		t.Errorf("revision: got %d want %d", redeploy.Revision, first.Revision+1)
	}

	// The app record itself must stay untouched by the redeploy.
	current, err := appStore.Get(app.ProjectID, app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if current.Image != "ghcr.io/acme/storefront:9.9.9" {
		t.Fatalf("RedeployApp must not write back to the app record: got %q", current.Image)
	}
}

func TestRedeployApp_UnknownDeployIsNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)

	_, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, "does-not-exist", "dev-1")
	if !errors.Is(err, apphost.ErrDeployNotFound) {
		t.Fatalf("expected ErrDeployNotFound, got %v", err)
	}
}

func TestRedeployApp_CrossAppDeployIsNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	otherApp := sampleDeployApp()
	otherApp.ID = "other-app"
	appStore := svc.apps.(*fakeAppStoreForDeploy)
	appStore.mu.Lock()
	appStore.apps[otherApp.ProjectID+"/"+otherApp.ID] = otherApp
	appStore.mu.Unlock()

	_, err = svc.RedeployApp(context.Background(), app.ProjectID, otherApp.ID, deploy.ID, "dev-1")
	if !errors.Is(err, apphost.ErrDeployNotFound) {
		t.Fatalf("expected ErrDeployNotFound for a deploy of a different app, got %v", err)
	}
}

func TestRedeployApp_CrossProjectDeployIsNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}

	_, err = svc.RedeployApp(context.Background(), "some-other-project", app.ID, deploy.ID, "dev-1")
	if !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("expected ErrAppNotFound for a project that holds no such app, got %v", err)
	}
}

func TestRedeployApp_AppNotFound(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)

	_, err := svc.RedeployApp(context.Background(), app.ProjectID, "does-not-exist", "dep-1", "dev-1")
	if !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("expected ErrAppNotFound, got %v", err)
	}
}

func TestRedeployApp_DeployLookupError(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, _ := newDeployTestService(t, app)
	deployStore.getErr = errors.New("db down")

	_, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, "dep-1", "dev-1")
	if err == nil || !strings.Contains(err.Error(), "look up deploy") {
		t.Fatalf("expected a wrapped lookup error, got %v", err)
	}
}

func TestRedeployApp_SupersedesARollingDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, kube := newDeployTestService(t, app)
	svc.async = func(f func()) { go f() }

	var calls int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	kube.AppRolloutFunc = func(ctx context.Context, ns, name string, timeout time.Duration) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		return nil
	}

	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("first DeployApp: %v", err)
	}
	<-firstStarted

	redeploy, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev-2")
	if err != nil {
		t.Fatalf("RedeployApp: %v", err)
	}
	close(releaseFirst)
	waitForDeployStatus(t, deployStore, redeploy.ID, apphost.DeployStatusSucceeded)

	stillSuperseded := deployStore.getByID(first.ID)
	if stillSuperseded == nil || stillSuperseded.Status != apphost.DeployStatusSuperseded {
		t.Fatalf("first deploy after being redeployed: got %+v, want it to stay superseded", stillSuperseded)
	}
}

func TestRedeployApp_FailedApplyMarksDeployFailed(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	source, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	kube.ApplyAppWorkloadErr = errors.New("apply refused")

	redeploy, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, source.ID, "dev-2")
	if err != nil {
		t.Fatalf("RedeployApp: %v", err)
	}
	if redeploy.Status != apphost.DeployStatusFailed {
		t.Fatalf("status: got %q want failed", redeploy.Status)
	}
	if !strings.Contains(redeploy.FailureReason, "apply refused") {
		t.Errorf("failure reason should name it: %q", redeploy.FailureReason)
	}
}

func TestSetStatus_LogsStoreErrorWithoutPanicking(t *testing.T) {
	app := sampleDeployApp()
	svc, deployStore, _ := newDeployTestService(t, app)
	deployStore.updateErr = errors.New("db down")

	deploy := &apphost.Deploy{ID: "dep-1"}
	svc.setStatus(deploy, apphost.DeployStatusSucceeded, "", nil)
	if deploy.Status != apphost.DeployStatusSucceeded {
		t.Errorf("in-memory status: got %q", deploy.Status)
	}
}

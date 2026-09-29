package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const tplProject = "proj-tpl"

// tplRecorder is one ordered log every fake writes to, so a test can read the order of effects.
type tplRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *tplRecorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *tplRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...)
}

func (r *tplRecorder) count(prefix string) int {
	n := 0
	for _, event := range r.all() {
		if strings.HasPrefix(event, prefix) {
			n++
		}
	}
	return n
}

type tplApps struct {
	log     *tplRecorder
	held    []*apphost.App
	failFor string
	maxSeen int
}

func (s *tplApps) Create(app *apphost.App, maxApps int) error {
	s.maxSeen = maxApps
	if app.Name == s.failFor {
		return apphost.ErrAppNameTaken
	}
	if len(s.held) >= maxApps {
		return apphost.AppLimitError{Limit: maxApps}
	}
	s.log.add("create %s", app.Name)
	s.held = append(s.held, app)
	return nil
}

func (s *tplApps) List(projectID string) ([]*apphost.App, error) {
	out := []*apphost.App{}
	for _, app := range s.held {
		if app.ProjectID == projectID {
			out = append(out, app)
		}
	}
	return out, nil
}

func (s *tplApps) byID(id string) *apphost.App {
	for _, app := range s.held {
		if app.ID == id {
			return app
		}
	}
	return nil
}

type tplDeployer struct {
	log        *tplRecorder
	busyOnce   map[string]bool
	apps       *tplApps
	failDeploy string
	errDeploy  string
	failDelete string
	admitErr   error
	admitted   []int
}

func (d *tplDeployer) DeployApp(_ context.Context, projectID, appID, _ string) (*apphost.Deploy, error) {
	app := d.apps.byID(appID)
	d.log.add("deploy %s", app.Name)
	if app.Name == d.errDeploy {
		return nil, errors.New("the lease could not be taken")
	}
	status, reason := apphost.DeployStatusRolling, ""
	if app.Name == d.failDeploy {
		status, reason = apphost.DeployStatusFailed, "apply app workload: forced"
	}
	return &apphost.Deploy{ID: "dep-" + app.Name, AppID: appID, ProjectID: projectID, Status: status, FailureReason: reason}, nil
}

func (d *tplDeployer) DeleteApp(_ context.Context, _, appID string, confirmDeleteDisk bool) error {
	app := d.apps.byID(appID)
	if app.Disk != nil && !confirmDeleteDisk {
		return ErrAppDiskDeleteUnconfirmed
	}
	if app.Name == d.failDelete {
		return errors.New("pods remain")
	}
	if d.busyOnce[app.Name] {
		d.busyOnce[app.Name] = false
		return ErrProjectOperationRunning
	}
	d.log.add("delete %s", app.Name)
	return nil
}

func (d *tplDeployer) AdmitNewApps(_ context.Context, _ string, replicas []int) error {
	d.admitted = replicas
	return d.admitErr
}

type tplNetwork struct {
	log *tplRecorder
	on  bool
	err error
	// failAfterOpen opens the network and then reports an error, as a failed read-back does.
	failAfterOpen bool
	busyCloses    int
}

func (n *tplNetwork) Describe(context.Context, string) (AppNetworkView, error) {
	return AppNetworkView{ProjectID: tplProject, PrivateNetwork: n.on, Applied: n.on}, nil
}

func (n *tplNetwork) Set(_ context.Context, _ string, enabled bool) (AppNetworkView, error) {
	if n.err != nil {
		return AppNetworkView{}, n.err
	}
	if !enabled && n.busyCloses > 0 {
		n.busyCloses--
		return AppNetworkView{}, ErrProjectOperationRunning
	}
	n.log.add("network %v", enabled)
	if enabled && n.failAfterOpen {
		n.on = true
		return AppNetworkView{}, errors.New("read the setting back: connection reset")
	}
	n.on = enabled
	return AppNetworkView{PrivateNetwork: enabled, Applied: enabled}, nil
}

type tplVault struct {
	log     *tplRecorder
	values  map[string]string
	putErr  error
	deleted []string
}

func (v *tplVault) Put(path string, data map[string]string) error {
	if v.putErr != nil {
		return v.putErr
	}
	v.log.add("secret %s", path)
	v.values[path] = data[apphost.AppSecretValueKey]
	return nil
}

func (v *tplVault) DeletePrefix(prefix string) (int, error) {
	v.log.add("purge %s", prefix)
	v.deleted = append(v.deleted, prefix)
	return 0, nil
}

type fixedAppLimit int

func (l fixedAppLimit) MaxApps(context.Context, string) (int, error) { return int(l), nil }

type fixedDiskCap int64

func (c fixedDiskCap) MaxDiskBytes(context.Context, string) (int64, error) { return int64(c), nil }

type tplRig struct {
	svc      *AppTemplateService
	log      *tplRecorder
	apps     *tplApps
	deployer *tplDeployer
	network  *tplNetwork
	vault    *tplVault
	project  *domain.DatabaseInstance
}

func newTemplateRig(t *testing.T) *tplRig {
	t.Helper()
	catalog, err := apptemplate.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	log := &tplRecorder{}
	apps := &tplApps{log: log}
	rig := &tplRig{
		log: log, apps: apps,
		deployer: &tplDeployer{log: log, apps: apps},
		network:  &tplNetwork{log: log},
		vault:    &tplVault{log: log, values: map[string]string{}},
		project: &domain.DatabaseInstance{ProjectID: tplProject, Namespace: "org1-" + tplProject, OrgID: "org1",
			DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive), DatabaseName: "proj_tpl"},
	}
	instances := fakestore.NewInstances()
	instances.Items[tplProject] = rig.project
	rig.svc = NewAppTemplateService(catalog, AppTemplateDeps{
		Apps: apps, Deployer: rig.deployer, Projects: instances, Plans: fixedPlan{tier: domain.Free},
		Limits: fixedAppLimit(2), Disks: fixedDiskCap(1 << 30), Network: rig.network, Secrets: rig.vault,
	})
	return rig
}

var adminConfirmed = TemplateDeployOptions{ConfirmPrivateNetwork: true, MayChangePrivateNetwork: true}

func (r *tplRig) expectNothingDone(t *testing.T) {
	t.Helper()
	if events := r.log.all(); len(events) != 0 {
		t.Fatalf("a refused template changed something: %v", events)
	}
}

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	var refused *TemplateRefusedError
	if !errors.As(err, &refused) || !errors.Is(err, ErrTemplateRefused) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal naming %q, got %v", want, err)
	}
}

func TestDeployTemplate_WebAndRedis(t *testing.T) {
	rig := newTemplateRig(t)
	result, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	events := rig.log.all()
	if events[0] != "network true" {
		t.Fatalf("the network opens before any app exists: %v", events)
	}
	if rig.log.count("create ") != 2 || rig.log.count("deploy ") != 2 || rig.log.count("delete ") != 0 {
		t.Fatalf("events %v", events)
	}
	if rig.apps.maxSeen != 2 {
		t.Fatalf("the store must hold the plan's limit, got %d", rig.apps.maxSeen)
	}
	if !result.PrivateNetworkTurnedOn || len(result.Apps) != 2 || result.Apps[0].Name != "redis" || result.Apps[1].DeployID != "dep-web" {
		t.Fatalf("result %+v", result)
	}
	redis, web := rig.apps.held[0], rig.apps.held[1]
	redisPassword := rig.vault.values[apphost.AppSecretRef(tplProject, redis.ID, "REDIS_PASSWORD").Path]
	webPassword := rig.vault.values[apphost.AppSecretRef(tplProject, web.ID, "REDIS_PASSWORD").Path]
	if len(redisPassword) != 32 || redisPassword != webPassword {
		t.Fatalf("one generated password in both apps' own entries: %q %q", redisPassword, webPassword)
	}
	if got := rig.vault.values[apphost.AppSecretRef(tplProject, web.ID, "REDIS_URL").Path]; got != "redis://:"+redisPassword+"@redis:6379/0" {
		t.Fatalf("REDIS_URL = %q", got)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", result, events), redisPassword) {
		t.Fatal("the generated password leaked into the result or the log")
	}
	if len(rig.deployer.admitted) != 2 {
		t.Fatalf("both apps are admitted together, got %v", rig.deployer.admitted)
	}
}

// Every secret value is stored before the app that points at it exists.
func TestDeployTemplate_SecretsBeforeTheirApp(t *testing.T) {
	rig := newTemplateRig(t)
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	events := rig.log.all()
	if len(events) != 4 || !strings.HasPrefix(events[1], "secret projects/"+tplProject+"/apps/") || events[2] != "create redis" {
		t.Fatalf("events %v", events)
	}
}

func TestDeployTemplate_OverThePlanIsRefusedBeforeAnything(t *testing.T) {
	rig := newTemplateRig(t)
	rig.apps.held = []*apphost.App{{ID: "a0", ProjectID: tplProject, Name: "existing"}}
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	refusedWith(t, err, "needs 2 apps")
	rig.expectNothingDone(t)
}

func TestDeployTemplate_ADiskAboveThePlanIsRefused(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.Disks = fixedDiskCap(512 << 20)
	_, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed)
	refusedWith(t, err, "plan allows up to 512Mi")
	rig.expectNothingDone(t)
}

func TestDeployTemplate_ANameTheProjectHoldsIsRefused(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.Limits = fixedAppLimit(5)
	rig.apps.held = []*apphost.App{{ID: "a0", ProjectID: tplProject, Name: "redis"}}
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	refusedWith(t, err, `already has an app named "redis"`)
	rig.expectNothingDone(t)
}

func TestDeployTemplate_NeedsAReadyDatabase(t *testing.T) {
	rig := newTemplateRig(t)
	rig.project.DatabaseName = ""
	rig.project.NoDatabase = true
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-postgres", "dev", TemplateDeployOptions{})
	refusedWith(t, err, "database")
	rig.expectNothingDone(t)
}

func TestDeployTemplate_WithADatabase(t *testing.T) {
	rig := newTemplateRig(t)
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "web-postgres", "dev", TemplateDeployOptions{}); err != nil {
		t.Fatalf("a developer deploys a template that needs no network: %v", err)
	}
	web := rig.apps.held[0]
	if ref := web.Env[0].Reference; ref == nil || ref.SourceName != "proj_tpl" {
		t.Fatalf("DATABASE_URL = %+v", web.Env[0])
	}
	if rig.log.count("network") != 0 {
		t.Fatal("a template without app-to-app traffic leaves the network alone")
	}
}

func TestDeployTemplate_TurningOnTheNetworkNeedsAnAdminWhoConfirms(t *testing.T) {
	rig := newTemplateRig(t)
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", TemplateDeployOptions{ConfirmPrivateNetwork: true})
	if !errors.Is(err, ErrTemplateNetworkNeedsAdmin) {
		t.Fatalf("a developer may not open the network: %v", err)
	}
	_, err = rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", TemplateDeployOptions{MayChangePrivateNetwork: true})
	if !errors.Is(err, ErrTemplateNetworkUnconfirmed) {
		t.Fatalf("an admin confirms first: %v", err)
	}
	rig.expectNothingDone(t)
}

func TestDeployTemplate_ANetworkAlreadyOnNeedsNoAdmin(t *testing.T) {
	rig := newTemplateRig(t)
	rig.network.on = true
	result, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", TemplateDeployOptions{})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if result.PrivateNetworkTurnedOn || rig.log.count("network") != 0 {
		t.Fatalf("the network was already on: %+v %v", result, rig.log.all())
	}
}

func TestDeployTemplate_NoPrivateNetworkOnThisPlatform(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.Network = nil
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	refusedWith(t, err, "private network")
	rig.expectNothingDone(t)
}

func TestDeployTemplate_NoRoomIsRefusedBeforeAnything(t *testing.T) {
	for _, admitErr := range []error{ErrAppCapacity, fmt.Errorf("%w (runtime class %q)", ErrAppNoSandboxNode, "gvisor"), &overPlanError{tier: domain.Free, allowed: 1}} {
		rig := newTemplateRig(t)
		rig.deployer.admitErr = admitErr
		_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
		refusedWith(t, err, admitErr.Error())
		rig.expectNothingDone(t)
	}
}

func TestDeployTemplate_TheStorageBudgetIsCheckedBeforeAnything(t *testing.T) {
	rig := newTemplateRig(t)
	mock := k8s.NewMockClient()
	rig.svc.deps.Budget = storagebudget.New(storagebudget.NewClusterSource(storagebudget.FixedCapacity(1<<30), mock), 80)
	_, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed)
	refusedWith(t, err, "storage budget")
	rig.expectNothingDone(t)
}

func TestDeployTemplate_AFailedDeployRollsEverythingBack(t *testing.T) {
	rig := newTemplateRig(t)
	rig.deployer.failDeploy = "web"
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "forced") {
		t.Fatalf("want the failure that stopped it, got %v", err)
	}
	events := rig.log.all()
	tail := events[len(events)-3:]
	if tail[0] != "delete web" || tail[1] != "delete redis" || tail[2] != "network false" {
		t.Fatalf("rollback in reverse, network last: %v", events)
	}
}

func TestDeployTemplate_AFailedCreateRollsBackAndForgetsItsSecrets(t *testing.T) {
	rig := newTemplateRig(t)
	rig.apps.failFor = "web"
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("want a rolled-back failure, got %v", err)
	}
	if rig.log.count("delete redis") != 1 || rig.log.count("deploy ") != 0 || rig.network.on {
		t.Fatalf("events %v", rig.log.all())
	}
	webPrefix := ""
	for _, prefix := range rig.vault.deleted {
		if !strings.Contains(prefix, rig.apps.held[0].ID) {
			webPrefix = prefix
		}
	}
	if webPrefix == "" {
		t.Fatalf("the secrets written for the app that was never created must go: %v", rig.vault.deleted)
	}
}

func TestDeployTemplate_ADeployErrorRollsBack(t *testing.T) {
	rig := newTemplateRig(t)
	rig.deployer.errDeploy = "redis"
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || rig.log.count("delete ") != 2 {
		t.Fatalf("got %v, events %v", err, rig.log.all())
	}
}

func TestDeployTemplate_ARollbackThatCannotFinishNamesWhatIsLeft(t *testing.T) {
	rig := newTemplateRig(t)
	rig.deployer.failDeploy = "web"
	rig.deployer.failDelete = "redis"
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var incomplete *TemplateRollbackError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), `"redis"`) {
		t.Fatalf("want an incomplete rollback naming redis, got %v", err)
	}
	if !rig.network.on {
		t.Fatal("the network stays on while an app that may use it is left")
	}
}

func TestDeployTemplate_ANetworkThatWillNotOpenCreatesNothing(t *testing.T) {
	rig := newTemplateRig(t)
	rig.network.err = errors.New("cilium said no")
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed); err == nil {
		t.Fatal("want an error")
	}
	rig.expectNothingDone(t)
}

func TestDeployTemplate_OneAtATimePerProject(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.Claimer = busyClaimer{}
	rig.svc.claimer = busyClaimer{}
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("got %v", err)
	}
	rig.expectNothingDone(t)
}

func TestDeployTemplate_UnknownTemplateAndProject(t *testing.T) {
	rig := newTemplateRig(t)
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "nope", "dev", adminConfirmed); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("unknown template: %v", err)
	}
	if _, err := rig.svc.Deploy(context.Background(), "proj-none", "redis", "dev", adminConfirmed); !errors.Is(err, ErrTemplateProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestDeployTemplate_NoVaultNoSecrets(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.Secrets = nil
	_, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed)
	refusedWith(t, err, "vault")
	rig.expectNothingDone(t)
}

func TestDescribeTemplates(t *testing.T) {
	rig := newTemplateRig(t)
	rig.apps.held = []*apphost.App{{ID: "a0", ProjectID: tplProject, Name: "existing"}}
	views, err := rig.svc.List(context.Background(), tplProject, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]TemplateView{}
	for _, view := range views {
		byID[view.ID] = view
		if view.Source != "" {
			t.Errorf("%s: the list leaves the source to the details", view.ID)
		}
	}
	webRedis := byID["web-redis"]
	fit := webRedis.Fit
	if fit.Plan != domain.Free || fit.AppsNeeded != 2 || fit.AppsHeld != 1 || fit.AppsAllowed != 2 ||
		fit.DiskBytes != 1<<30 || fit.DiskCapBytes != 1<<30 || fit.AppCPU == "" || fit.AppMemory == "" {
		t.Fatalf("fit %+v", fit)
	}
	if !webRedis.NeedsPrivateNetwork || fit.PrivateNetworkOn || fit.CanTurnOnPrivateNetwork || len(fit.Refusals) != 1 {
		t.Fatalf("web-redis %+v", webRedis)
	}
	for _, app := range webRedis.Apps {
		for _, v := range app.Env {
			if v.Name == "REDIS_PASSWORD" && app.Name == "redis" && v.Source != "generated" {
				t.Errorf("redis password source %q", v.Source)
			}
			if v.Name == "REDIS_HOST" && (v.Source != "app" || v.Value != "") {
				t.Errorf("REDIS_HOST %+v", v)
			}
		}
	}
	if len(byID["redis"].Fit.Refusals) != 0 || !byID["web-postgres"].Fit.DatabaseReady {
		t.Fatalf("redis %+v web-postgres %+v", byID["redis"].Fit, byID["web-postgres"].Fit)
	}

	one, err := rig.svc.Get(context.Background(), tplProject, "redis", true)
	if err != nil || !strings.Contains(one.Source, "format: excalibase.template/v1") || !one.Fit.CanTurnOnPrivateNetwork {
		t.Fatalf("get: %v %+v", err, one)
	}
}

// A rollout that finishes as the rollback starts holds the app's lease for a moment.
func TestDeployTemplate_RollbackWaitsOutABriefLease(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.leaseRetry = 0
	rig.deployer.failDeploy = "web"
	rig.deployer.busyOnce = map[string]bool{"redis": true}
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || rig.log.count("delete redis") != 1 {
		t.Fatalf("got %v, events %v", err, rig.log.all())
	}
}

// An error after the cluster opened the network still closes it again.
func TestDeployTemplate_ANetworkErrorAfterOpeningClosesItAgain(t *testing.T) {
	rig := newTemplateRig(t)
	rig.network.failAfterOpen = true
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || rig.network.on || rig.log.count("create ") != 0 {
		t.Fatalf("got %v, network on=%v, events %v", err, rig.network.on, rig.log.all())
	}
}

func TestDeployTemplate_RollbackWaitsOutABusyProjectToCloseTheNetwork(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.leaseRetry = 0
	rig.deployer.failDeploy = "web"
	rig.network.busyCloses = 2
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || rig.network.on {
		t.Fatalf("got %v, network on=%v", err, rig.network.on)
	}
}

// What a caller is told names the step, never a store's or the cluster's own words.
func TestDeployTemplate_AFailureTellsTheStepNotTheInternals(t *testing.T) {
	rig := newTemplateRig(t)
	rig.deployer.errDeploy = "redis"
	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	var failed *TemplateFailedError
	if !errors.As(err, &failed) || !failed.ServerFault || strings.Contains(failed.Public(), "lease") ||
		!strings.Contains(failed.Public(), `could not deploy app "redis"`) {
		t.Fatalf("got %+v", failed)
	}

	rig = newTemplateRig(t)
	rig.deployer.failDeploy = "web"
	_, err = rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	if !errors.As(err, &failed) || failed.ServerFault || !strings.Contains(failed.Public(), "apply app workload: forced") {
		t.Fatalf("a deploy's own failure reason is the customer's to read: %+v", failed)
	}

	rig = newTemplateRig(t)
	rig.apps.failFor = "web"
	_, err = rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	if !errors.As(err, &failed) || failed.ServerFault || !strings.Contains(failed.Public(), apphost.ErrAppNameTaken.Error()) {
		t.Fatalf("a name taken meanwhile is a conflict: %+v", failed)
	}
}

func TestDeployTemplate_ASecretThatCannotBeDrawnIsNotARefusal(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.secret = func(int) (string, error) { return "", errors.New("entropy exhausted") }
	_, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed)
	if err == nil || errors.Is(err, ErrTemplateRefused) {
		t.Fatalf("a failed draw is a server fault, got %v", err)
	}
	rig.expectNothingDone(t)
}

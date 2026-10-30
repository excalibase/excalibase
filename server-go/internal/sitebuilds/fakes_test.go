package sitebuilds

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

const (
	testProject = "proj-a"
	testApp     = "app-1"
	testSite    = "site-1"
	testUser    = "u-dev"
	testCommit  = "1111111111111111111111111111111111111111"
	testInstall = int64(7)
	testRepoID  = int64(70)
	testRepo    = "acme/api"
)

// memoryStore keeps the queue in memory; methods the tests never reach stay on the nil Store and panic.
type memoryStore struct {
	Store
	mu              sync.Mutex
	siteConnections map[string]*Connection
	appConnections  map[Target]*AppConnection
	builds          []*Build
	visible         []string
	grants          []gitprovider.Access
	deliveries      map[string]bool
	claimedKinds    [][]string
	logs            map[string]string
	cancelled       []Target
	deactivated     []Target
	lost            []LostBuild
}

func newMemoryStore() *memoryStore {
	return &memoryStore{siteConnections: map[string]*Connection{}, appConnections: map[Target]*AppConnection{},
		deliveries: map[string]bool{}, logs: map[string]string{}}
}

func (m *memoryStore) GetConnection(_ context.Context, projectID, siteID string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection := m.siteConnections[siteID]
	if connection == nil || connection.ProjectID != projectID {
		return nil, nil
	}
	copied := *connection
	return &copied, nil
}

func (m *memoryStore) ConnectionsForPush(_ context.Context, _ string, installationID, repositoryID int64, branch string) ([]*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []*Connection
	for _, connection := range m.siteConnections {
		if connection.InstallationID == installationID && connection.RepositoryID == repositoryID && connection.Settings.Branch == branch {
			copied := *connection
			found = append(found, &copied)
		}
	}
	return found, nil
}

func (m *memoryStore) GetAppConnection(_ context.Context, projectID, appID string) (*AppConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection := m.appConnections[Target{Kind: KindApp, ProjectID: projectID, ID: appID}]
	if connection == nil {
		return nil, nil
	}
	copied := *connection
	return &copied, nil
}

func (m *memoryStore) SaveAppConnection(_ context.Context, connection *AppConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *connection
	m.appConnections[connection.target()] = &copied
	return nil
}

func (m *memoryStore) DeleteAppConnection(_ context.Context, projectID, appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := Target{Kind: KindApp, ProjectID: projectID, ID: appID}
	if m.appConnections[target] == nil {
		return ErrNotConnected
	}
	delete(m.appConnections, target)
	return nil
}

func (m *memoryStore) AppConnectionsForPush(_ context.Context, _ string, installationID, repositoryID int64, branch string) ([]*AppConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []*AppConnection
	for _, connection := range m.appConnections {
		if connection.InstallationID == installationID && connection.RepositoryID == repositoryID && connection.Settings.Branch == branch {
			copied := *connection
			found = append(found, &copied)
		}
	}
	return found, nil
}

func (m *memoryStore) CancelBuilds(_ context.Context, target Target, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelled = append(m.cancelled, target)
	return nil
}

func (m *memoryStore) DeactivateConnection(_ context.Context, target Target, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deactivated = append(m.deactivated, target)
	if connection := m.appConnections[target]; connection != nil {
		connection.InactiveReason = reason
	}
	return nil
}

func (m *memoryStore) RenameRepository(_ context.Context, target Target, _ int64, fullName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if connection := m.appConnections[target]; connection != nil {
		connection.Repository = fullName
	}
	return nil
}

func (m *memoryStore) Grants(context.Context, string, string, time.Time) ([]gitprovider.Access, error) {
	return m.grants, nil
}

func (m *memoryStore) DeliverySeen(_ context.Context, _, deliveryID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deliveries[deliveryID], nil
}

func (m *memoryStore) RecordDelivery(_ context.Context, _, deliveryID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries[deliveryID] = true
	return nil
}

func (m *memoryStore) DeliveryQueued(_ context.Context, target Target, deliveryID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, build := range m.builds {
		if build.Target() == target && build.DeliveryID == deliveryID {
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryStore) Enqueue(_ context.Context, build *Build, _ int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *build
	m.builds = append(m.builds, &copied)
	return true, nil
}

func (m *memoryStore) ClaimNext(_ context.Context, _ int, _ string, _ time.Time, kinds []string) (*Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimedKinds = append(m.claimedKinds, kinds)
	for _, build := range m.builds {
		if build.Status == StatusQueued && slices.Contains(kinds, build.Kind) {
			build.Status = StatusRunning
			copied := *build
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *memoryStore) ExpireLostBuilds(context.Context, time.Time, string) ([]LostBuild, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lost := m.lost
	m.lost = nil
	return lost, nil
}

func (m *memoryStore) ExtendLease(context.Context, string, string, time.Time) error { return nil }

func (m *memoryStore) SaveLog(_ context.Context, buildID, _, log string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs[buildID] = log
	return nil
}

func (m *memoryStore) FinishBuild(_ context.Context, build *Build, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, held := range m.builds {
		if held.ID == build.ID {
			copied := *build
			m.builds[i] = &copied
		}
	}
	return nil
}

func (m *memoryStore) ListBuilds(_ context.Context, target Target, _ int) ([]*Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []*Build
	for _, build := range m.builds {
		if build.Target() == target {
			found = append(found, build)
		}
	}
	return found, nil
}

func (m *memoryStore) GetBuild(_ context.Context, target Target, id string) (*Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, build := range m.builds {
		if build.Target() == target && build.ID == id {
			copied := *build
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *memoryStore) VisibleSecrets(context.Context, string) ([]string, error) {
	return m.visible, nil
}

func (m *memoryStore) build(id string) *Build {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, build := range m.builds {
		if build.ID == id {
			copied := *build
			return &copied
		}
	}
	return nil
}

func (m *memoryStore) queuedOf(kind string) []*Build {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []*Build
	for _, build := range m.builds {
		if build.Kind == kind {
			found = append(found, build)
		}
	}
	return found
}

// fakeProvider is a git host whose answers the test sets.
type fakeProvider struct {
	mu       sync.Mutex
	minted   int
	revoked  []string
	head     gitprovider.Commit
	repo     gitprovider.Repository
	files    map[string]bool
	filesErr error
	checked  []string
	event    gitprovider.Event
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{head: gitprovider.Commit{SHA: testCommit, Message: "feat: api"},
		repo: gitprovider.Repository{ID: testRepoID, FullName: testRepo, DefaultBranch: "main"}, files: map[string]bool{}}
}

func (f *fakeProvider) Name() string { return "github" }
func (f *fakeProvider) AuthorizeURL(state, _ string) string {
	return "https://github.test/login?state=" + state
}
func (f *fakeProvider) InstallURL(state string) string {
	return "https://github.test/install?state=" + state
}

func (f *fakeProvider) UserAccess(context.Context, string, string) ([]gitprovider.Access, error) {
	return nil, nil
}

func (f *fakeProvider) Repository(context.Context, int64, int64) (gitprovider.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repo, nil
}

func (f *fakeProvider) BranchHead(context.Context, int64, gitprovider.Repository, string) (gitprovider.Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

func (f *fakeProvider) CloneCredential(context.Context, int64, int64) (gitprovider.CloneCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted++
	return gitprovider.CloneCredential{Token: fmt.Sprintf("clone-token-%d", f.minted)}, nil
}

func (f *fakeProvider) RevokeCloneCredential(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return nil
}

func (f *fakeProvider) CloneURL(repository gitprovider.Repository) string {
	return "https://github.test/" + repository.FullName + ".git"
}

func (f *fakeProvider) ParseWebhook(http.Header, []byte) (gitprovider.Event, error) {
	return f.event, nil
}

func (f *fakeProvider) FileExists(_ context.Context, _ int64, _ gitprovider.Repository, _, path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, path)
	return f.files[path], f.filesErr
}

func (f *fakeProvider) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// fakeJobs plays each started build's outcome: one status with the clone done, then the outcome.
type fakeJobs struct {
	mu        sync.Mutex
	outcomes  []RunStatus
	current   RunStatus
	reads     int
	removed   []string
	forgot    []string
	forgetErr error
	logText   string
}

func (f *fakeJobs) begin() {
	f.current = RunStatus{Phase: RunReady}
	if len(f.outcomes) > 0 {
		f.current, f.outcomes = f.outcomes[0], f.outcomes[1:]
	}
	f.reads = 0
}

func (f *fakeJobs) Status(context.Context, string) (RunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.reads == 1 {
		return RunStatus{Phase: RunRunning, CloneDone: true}, nil
	}
	return f.current, nil
}

func (f *fakeJobs) Logs(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logText, nil
}

func (f *fakeJobs) Remove(_ context.Context, buildID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, buildID)
	return nil
}

func (f *fakeJobs) ForgetCloneCredential(_ context.Context, buildID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, buildID)
	return f.forgetErr
}

func (f *fakeJobs) removedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakeJobs) forgotIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forgot...)
}

type fakeAppRunner struct {
	fakeJobs
	runs     []AppRun
	startErr error
}

func (f *fakeAppRunner) StartApp(_ context.Context, run AppRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	f.begin()
	return f.startErr
}

func (f *fakeAppRunner) FailureMessage(status RunStatus, _ *Build) string {
	return "the app build failed: " + string(status.Failure)
}

func (f *fakeAppRunner) started() []AppRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AppRun(nil), f.runs...)
}

type fakeSiteRunner struct {
	fakeJobs
	specs []RunSpec
}

func (f *fakeSiteRunner) Start(_ context.Context, spec RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	f.begin()
	return nil
}

func (f *fakeSiteRunner) Collect(_ context.Context, _ string, w io.Writer, _ int64) error {
	_, err := w.Write([]byte("archive"))
	return err
}

func (f *fakeSiteRunner) started() []RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunSpec(nil), f.specs...)
}

type fakePublisher struct {
	mu        sync.Mutex
	published []AppPublish
	err       error
}

func (f *fakePublisher) PublishBuild(_ context.Context, publish AppPublish) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, publish)
	if f.err != nil {
		return "", f.err
	}
	return "deploy-" + publish.BuildID[:4], nil
}

// fakeSites is a project with one site whose deploys always succeed.
type fakeSites struct{}

func (fakeSites) Get(_ context.Context, projectID, id string) (*sites.Site, error) {
	if projectID != testProject || id != testSite {
		return nil, sites.ErrSiteNotFound
	}
	return &sites.Site{ID: id, ProjectID: projectID}, nil
}

func (fakeSites) Deploy(context.Context, sites.DeployRequest) (*sites.Deploy, error) {
	return &sites.Deploy{ID: "site-deploy"}, nil
}

func (fakeSites) MaxUploadBytes(context.Context, string) (int64, error) { return 1 << 20, nil }
func (fakeSites) TakeDeploySlot(context.Context, string) error          { return nil }

// fakeApps holds one app in testProject; like the Postgres store it answers nil for any other.
type fakeApps struct{}

func (fakeApps) Get(projectID, id string) (*apphost.App, error) {
	if projectID != testProject || id != testApp {
		return nil, nil
	}
	return &apphost.App{ID: id, ProjectID: projectID, Name: "api"}, nil
}

type fakeLimits struct{}

var (
	testSiteLimits = Limits{CPUMillis: 1000, Memory: "2Gi", Disk: "5Gi", TimeoutSeconds: 60, ProjectConcurrency: 1}
	testAppLimits  = AppLimits{Limits: Limits{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 120, ProjectConcurrency: 2},
		MaxImageBytes: 2 << 30}
)

func (fakeLimits) BuildLimits(context.Context, string) (Limits, error) { return testSiteLimits, nil }
func (fakeLimits) LargestBuild(context.Context) (BuildSize, error) {
	return BuildSize{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi"}, nil
}
func (fakeLimits) AppBuildLimits(context.Context, string) (AppLimits, error) {
	return testAppLimits, nil
}
func (fakeLimits) LargestAppBuild(context.Context) (BuildSize, error) {
	return BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"}, nil
}

type fakeSecrets map[string]string

func (f fakeSecrets) Secrets(context.Context, string) (map[string]string, error) { return f, nil }

// testBed is a service and dispatcher over the fakes, app builds switched on.
type testBed struct {
	store      *memoryStore
	provider   *fakeProvider
	service    *Service
	dispatcher *Dispatcher
	sites      *fakeSiteRunner
	apps       *fakeAppRunner
	publisher  *fakePublisher
}

func newTestBed(appsOn bool) *testBed {
	store, provider := newMemoryStore(), newFakeProvider()
	store.grants = []gitprovider.Access{{Installation: gitprovider.Installation{ID: testInstall, Account: "acme"},
		Repositories: []gitprovider.Repository{provider.repo}}}
	service := NewService(store, provider, fakeSites{}, fakeLimits{}, fakeSecrets{"API_KEY": "sk-live-very-secret"},
		Options{Mode: ModeOn, NodeImage: "node"})
	flags := features.NewStatic()
	if appsOn {
		flags = features.NewStatic(features.AppGitBuilds)
	}
	service.EnableApps(AppSupport{Apps: fakeApps{}, Limits: fakeLimits{}, Files: provider, Flags: flags})
	siteRunner, appRunner, publisher := &fakeSiteRunner{}, &fakeAppRunner{}, &fakePublisher{}
	dispatcher := NewDispatcher(service, siteRunner, DispatcherOptions{Holder: "replica", StatusPoll: time.Millisecond,
		LogEvery: time.Millisecond, Lease: time.Minute, PendingGrace: time.Second, DeployRetry: time.Millisecond, ShutdownGrace: time.Second})
	dispatcher.EnableApps(appRunner, publisher)
	return &testBed{store: store, provider: provider, service: service, dispatcher: dispatcher, sites: siteRunner, apps: appRunner, publisher: publisher}
}

// connectApp saves an app connection directly, as a developer's earlier connect would have.
func (b *testBed) connectApp(settings AppSettings) *AppConnection {
	if settings.Branch == "" {
		settings.Branch = "main"
	}
	if settings.Builder == "" {
		settings.Builder = AppBuilderAuto
	}
	connection := &AppConnection{AppID: testApp, GitSource: GitSource{ProjectID: testProject, Provider: "github",
		InstallationID: testInstall, RepositoryID: testRepoID, Repository: testRepo, ConnectedBy: testUser}, Settings: settings}
	_ = b.store.SaveAppConnection(context.Background(), connection)
	return connection
}

func (b *testBed) connectSite() {
	b.store.siteConnections[testSite] = &Connection{SiteID: testSite, GitSource: GitSource{ProjectID: testProject, Provider: "github",
		InstallationID: testInstall, RepositoryID: testRepoID, Repository: testRepo, ConnectedBy: testUser},
		Settings: Settings{Branch: "main", OutputDir: "dist", NodeVersion: DefaultNodeVersion}}
}

func (b *testBed) runAll() {
	b.dispatcher.Tick(context.Background())
	b.dispatcher.Wait()
}

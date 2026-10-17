//go:build integration

// Package sitebuildstest runs git builds of sites end to end against a real
// platform database, the real Sites service and a fake GitHub, with a fake cluster.
package sitebuildstest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/gitprovider/githubapp"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

const (
	HeadSHA     = "1111111111111111111111111111111111111111"
	CallbackURL = "https://api.example.com/api/sites/github/callback"
	// siteRepoName is the repository the harness site builds from.
	siteRepoName = "acme/site"
)

var (
	sharedOnce sync.Once
	sharedDB   *postgres.Store
	sharedErr  error
)

// One migrated platform database serves the package; every test uses its own project and sites.
func platformDB(t *testing.T) *postgres.Store {
	t.Helper()
	sharedOnce.Do(func() { sharedDB, sharedErr = startPlatformDB() })
	if sharedErr != nil {
		t.Fatalf("start platform database: %v", sharedErr)
	}
	return sharedDB
}

func startPlatformDB() (*postgres.Store, error) {
	ctx := context.Background()
	password := testutil.FixturePassword("sitebuilds-itest")
	container, err := pgmod.Run(ctx, "postgres:16-alpine", pgmod.WithDatabase("platform_test"), pgmod.WithUsername("platform"),
		pgmod.WithPassword(password), testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		return nil, err
	}
	host, err := container.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword("platform", password),
		Host: fmt.Sprintf("%s:%s", host, port.Port()), Path: "platform_test", RawQuery: "sslmode=disable"}
	return postgres.New(dsn.String())
}

var nextIDs atomic.Int64

// settleQueue finishes whatever earlier tests left queued or running, so a claim here sees only this test's builds.
func settleQueue(t *testing.T, db *postgres.Store) {
	t.Helper()
	if _, err := db.DB().Exec(`UPDATE site_builds SET status = 'CANCELLED', finished_at = NOW(), lease_holder = NULL, lease_until = NULL
		WHERE status IN ('QUEUED', 'RUNNING')`); err != nil {
		t.Fatalf("settle the queue: %v", err)
	}
}

// fixedLimits sizes every build the same.
type fixedLimits struct {
	limits sitebuilds.Limits
	err    error
}

func (f fixedLimits) BuildLimits(context.Context, string) (sitebuilds.Limits, error) {
	return f.limits, f.err
}

func (f fixedLimits) LargestBuild(context.Context) (sitebuilds.BuildSize, error) {
	return sitebuilds.BuildSize{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi"}, f.err
}

var FreeLimits = sitebuilds.Limits{CPUMillis: 2000, Memory: "2Gi", Disk: "5Gi", TimeoutSeconds: 600, ProjectConcurrency: 1}

type memorySecrets struct {
	values map[string]string
	err    error
}

func (m memorySecrets) Secrets(context.Context, string) (map[string]string, error) {
	return m.values, m.err
}

// siteLimiter holds every project to a deploy rate the test sets.
type siteLimiter struct{ perHour int }

func (l siteLimiter) DeployLimits(context.Context, string) (sites.Limits, error) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = l.perHour
	return limits, nil
}

type Harness struct {
	t *testing.T
	// installation, siteRepo and privateRepo are this harness's own, so pushes match only its sites.
	Installation int64
	SiteRepo     int64
	PrivateRepo  int64
	Project      string
	Site         *sites.Site
	Sites        *sites.Service
	Publisher    *sitestest.Publisher
	GitHub       *githubfake.GitHub
	Store        *sitebuilds.PostgresStore
	Service      *sitebuilds.Service
	Runner       *FakeRunner
	Dispatcher   *sitebuilds.Dispatcher
	User         string
}

type Options struct {
	Members *FakeMembers
	PerHour int
	Secrets map[string]string
	Limits  sitebuilds.Limits
	Slots   int
}

func New(t *testing.T, options Options) *Harness {
	t.Helper()
	db := platformDB(t)
	if options.PerHour == 0 {
		options.PerHour = 30
	}
	if options.Limits.CPUMillis == 0 {
		options.Limits = FreeLimits
	}
	publisher := sitestest.NewPublisher()
	siteService, err := sites.NewService(sites.NewPostgresStore(db.DB()), publisher, sitestest.NewOrigins(),
		sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir(), DeployLimits: siteLimiter{perHour: options.PerHour}})
	if err != nil {
		t.Fatal(err)
	}
	project := "proj-" + uuid.NewString()[:8]
	site, err := siteService.Create(context.Background(), project, "s-"+uuid.NewString()[:8], nil, "u-owner")
	if err != nil {
		t.Fatal(err)
	}
	settleQueue(t, db)
	ids := nextIDs.Add(10)
	installation, siteRepo, privateRepo := ids, ids+1, ids+2
	fake := githubfake.New(t)
	fake.AddInstallation(installation, "acme",
		&githubfake.Repo{ID: siteRepo, FullName: siteRepoName, DefaultBranch: "main", Branches: map[string]string{"main": HeadSHA}},
		&githubfake.Repo{ID: privateRepo, FullName: "acme/payroll", DefaultBranch: "main", Branches: map[string]string{"main": HeadSHA}})
	client, err := githubapp.New(githubapp.Config{AppID: githubfake.AppID, AppSlug: githubfake.AppSlug, ClientID: githubfake.ClientID,
		ClientSecret: githubfake.ClientSecret, PrivateKeyPEM: fake.PrivateKeyPEM, WebhookSecret: fake.WebhookSecret,
		APIURL: fake.URL(), WebURL: fake.URL()})
	if err != nil {
		t.Fatal(err)
	}
	store := sitebuilds.NewPostgresStore(db.DB())
	serviceOptions := sitebuilds.Options{Mode: sitebuilds.ModeOn, CallbackURL: CallbackURL, NodeImage: "node"}
	if options.Members != nil {
		serviceOptions.Members = options.Members
	}
	service := sitebuilds.NewService(store, client, siteService, fixedLimits{limits: options.Limits},
		memorySecrets{values: options.Secrets}, serviceOptions)
	runner := newFakeRunner(t)
	if options.Slots == 0 {
		options.Slots = 100
	}
	dispatcher := sitebuilds.NewDispatcher(service, runner, sitebuilds.DispatcherOptions{
		Slots: options.Slots, Holder: "replica-" + project, SpoolDir: t.TempDir(), StatusPoll: 10 * time.Millisecond,
		LogEvery: 10 * time.Millisecond, DeployRetry: 10 * time.Millisecond, Lease: 30 * time.Second,
		PendingGrace: 200 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond,
	})
	return &Harness{t: t, Installation: installation, SiteRepo: siteRepo, PrivateRepo: privateRepo, Project: project, Site: site, Sites: siteService, Publisher: publisher, GitHub: fake, Store: store,
		Service: service, Runner: runner, Dispatcher: dispatcher, User: "u-" + project}
}

// SignIn runs the GitHub sign-in for the harness user, who sees only acme/site.
func (h *Harness) SignIn() {
	h.t.Helper()
	started, err := h.Service.StartSignIn(context.Background(), h.User, h.Project, h.Site.ID, false)
	if err != nil {
		h.t.Fatalf("start sign-in: %v", err)
	}
	parsed, err := url.Parse(started.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	code := "code-" + h.Project
	h.GitHub.AddSignIn(code, map[int64][]int64{h.Installation: {h.SiteRepo}})
	if _, err := h.Service.FinishSignIn(context.Background(), parsed.Query().Get("state"), started.BrowserKey, code); err != nil {
		h.t.Fatalf("finish sign-in: %v", err)
	}
}

func (h *Harness) Connect() *sitebuilds.ConnectResult {
	h.t.Helper()
	h.SignIn()
	result, err := h.Service.Connect(context.Background(), sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID,
		UserID: h.User, Repository: siteRepoName, Settings: sitebuilds.Settings{OutputDir: "dist"}})
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	return result
}

// Push moves the branch on GitHub, then delivers the signed push for it.
func (h *Harness) Push(branch, sha, delivery string) (int, error) {
	h.GitHub.SetBranchHead(h.SiteRepo, branch, sha)
	return h.Deliver(branch, sha, delivery)
}

// Deliver sends a signed push without moving the branch, as a replayed delivery would.
func (h *Harness) Deliver(branch, sha, delivery string) (int, error) {
	body := githubfake.PushPayload(h.Installation, h.SiteRepo, siteRepoName, branch, sha, "feat: "+sha[:4])
	header := http.Header{"X-Github-Event": {"push"}, "X-Github-Delivery": {delivery}, "X-Hub-Signature-256": {h.GitHub.Sign(body)}}
	return h.Service.HandleWebhook(context.Background(), header, body)
}

func (h *Harness) Builds() []*sitebuilds.Build {
	h.t.Helper()
	builds, err := h.Service.ListBuilds(context.Background(), h.Project, h.Site.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return builds
}

func (h *Harness) Build(id string) *sitebuilds.Build {
	h.t.Helper()
	build, err := h.Service.GetBuild(context.Background(), h.Project, h.Site.ID, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return build
}

// RunAll starts what the caps allow and waits for those builds to finish.
func (h *Harness) RunAll() {
	h.Dispatcher.Tick(context.Background())
	h.Dispatcher.Wait()
}

// FakeMembers answers who connected a repository; a user listed in gone lost the developer rung.
type FakeMembers struct {
	mu   sync.Mutex
	gone map[string]bool
}

func (f *FakeMembers) Standing(_ context.Context, userID, _ string) (sitebuilds.MemberStanding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sitebuilds.MemberStanding{Name: "name of " + userID, CanDeploy: !f.gone[userID]}, nil
}

func (f *FakeMembers) Remove(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone == nil {
		f.gone = map[string]bool{}
	}
	f.gone[userID] = true
}

// FakeRunner stands in for the cluster: a build reaches the status its script names.
type FakeRunner struct {
	t        *testing.T
	mu       sync.Mutex
	started  []sitebuilds.RunSpec
	removed  []string
	Outcome  sitebuilds.RunStatus
	Archive  []byte
	LogText  string
	StartErr error
	// statusErr answers every status read, as an unreachable API server would.
	StatusErr error
}

func newFakeRunner(t *testing.T) *FakeRunner {
	return &FakeRunner{t: t, Outcome: sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, Archive: sitestest.Dist(t, "<h1>built</h1>"),
		LogText: "== build ==\nvite v8 building for production...\n"}
}

func (f *FakeRunner) Start(_ context.Context, spec sitebuilds.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	return f.StartErr
}

func (f *FakeRunner) Status(context.Context, string) (sitebuilds.RunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Outcome, f.StatusErr
}

func (f *FakeRunner) Set(outcome sitebuilds.RunStatus, statusErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Outcome, f.StatusErr = outcome, statusErr
}

func (f *FakeRunner) RemovedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *FakeRunner) Collect(_ context.Context, _ string, w io.Writer, limit int64) error {
	if int64(len(f.Archive)) > limit {
		return sitebuilds.ErrOutputTooLarge
	}
	_, err := w.Write(f.Archive)
	return err
}

func (f *FakeRunner) Logs(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.LogText, nil
}

func (f *FakeRunner) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func (f *FakeRunner) Specs() []sitebuilds.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sitebuilds.RunSpec(nil), f.started...)
}

func MustNotFind(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("%q found in %q", needle, haystack)
	}
}

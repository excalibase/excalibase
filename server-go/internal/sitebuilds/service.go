package sitebuilds

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

const (
	// signInLifetime bounds a sign-in link; grantLifetime how long its listing may be used to connect.
	signInLifetime  = 10 * time.Minute
	grantLifetime   = time.Hour
	buildsKept      = 30
	buildsListLimit = 30
	stateBytes      = 32
)

// Mode is whether this install offers git builds.
type Mode int

const (
	ModeOff Mode = iota
	// ModeSingleHost refuses by name: git builds need the cluster sandbox.
	ModeSingleHost
	ModeOn
)

// Sites is the Sites service a build deploys through, the same path an upload takes.
type Sites interface {
	Get(ctx context.Context, projectID, id string) (*sites.Site, error)
	Deploy(ctx context.Context, request sites.DeployRequest) (*sites.Deploy, error)
	MaxUploadBytes(ctx context.Context, projectID string) (int64, error)
	TakeDeploySlot(ctx context.Context, projectID string) error
}

// Options are the install's settings for git builds.
type Options struct {
	Mode Mode
	// CallbackURL is where the git host sends a signed-in user back to.
	CallbackURL string
	// NodeImage is the image repository builds run in.
	NodeImage string
	// Members answers whether a connector may still deploy the project; nil trusts every connection.
	Members MembershipChecker
}

// MembershipChecker answers who a project member is and whether they still hold the developer rung.
type MembershipChecker interface {
	Standing(ctx context.Context, userID, projectID string) (MemberStanding, error)
}

type MemberStanding struct {
	Name      string
	CanDeploy bool
}

// noNudge serves until a dispatcher registers; its next poll finds the build anyway.
func noNudge() {
	// no dispatcher in this process to wake
}

// Service runs git connections and the build queue's front: everything but running builds.
type Service struct {
	store    Store
	provider gitprovider.Provider
	sites    Sites
	limits   LimitsSource
	secrets  SecretReader
	options  Options
	now      func() time.Time
	// nudge wakes the dispatcher when a build is queued; nil when none runs here.
	nudge func()
	// apps is set once app builds are wired; the appgitbuilds flag still gates every use.
	apps *AppSupport
}

func NewService(store Store, provider gitprovider.Provider, siteService Sites, limits LimitsSource, secrets SecretReader, options Options) *Service {
	return &Service{store: store, provider: provider, sites: siteService, limits: limits, secrets: secrets, options: options,
		now: time.Now, nudge: noNudge}
}

// SetNow replaces the clock, for tests.
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// Available answers why git builds cannot be used here, or nil.
func (s *Service) Available() error {
	switch {
	case s == nil || s.options.Mode == ModeOff:
		return ErrUnavailable
	case s.options.Mode == ModeSingleHost:
		return ErrSingleHost
	}
	return nil
}

func (s *Service) mustSite(ctx context.Context, projectID, siteID string) error {
	if err := s.Available(); err != nil {
		return err
	}
	_, err := s.sites.Get(ctx, projectID, siteID)
	return err
}

// StartSignIn answers the git host's sign-in (or install) link for a user connecting a site.
// StartedSignIn is the git host's link, and the key the starting browser keeps
// (a cookie) so the callback is honoured only in that browser.
type StartedSignIn struct {
	URL        string
	BrowserKey string
}

func (s *Service) StartSignIn(ctx context.Context, userID, projectID, siteID string, install bool) (StartedSignIn, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return StartedSignIn{}, err
	}
	return s.startSignIn(ctx, SignIn{UserID: userID, ProjectID: projectID, SiteID: siteID}, install)
}

// startSignIn records the single-use state for the site or app the sign-in returns to.
func (s *Service) startSignIn(ctx context.Context, signIn SignIn, install bool) (StartedSignIn, error) {
	state, err := randomToken()
	if err != nil {
		return StartedSignIn{}, err
	}
	browserKey, err := randomToken()
	if err != nil {
		return StartedSignIn{}, err
	}
	signIn.BrowserHash = hashState(browserKey)
	if err := s.store.SaveSignIn(ctx, hashState(state), signIn, s.now().Add(signInLifetime)); err != nil {
		return StartedSignIn{}, err
	}
	link := s.provider.AuthorizeURL(state, s.options.CallbackURL)
	if install {
		link = s.provider.InstallURL(state)
	}
	return StartedSignIn{URL: link, BrowserKey: browserKey}, nil
}

func randomToken() (string, error) {
	raw := make([]byte, stateBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("make a sign-in state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// FinishSignIn takes the state once, then records what the user's own sign-in shows them.
// The state is spent even when the browser key does not match, so a lured link cannot be retried.
func (s *Service) FinishSignIn(ctx context.Context, state, browserKey, code string) (SignIn, error) {
	if err := s.Available(); err != nil {
		return SignIn{}, err
	}
	if state == "" || code == "" {
		return SignIn{}, ErrStateInvalid
	}
	signIn, err := s.store.TakeSignIn(ctx, hashState(state), s.now())
	if err != nil {
		return SignIn{}, err
	}
	if subtle.ConstantTimeCompare([]byte(signIn.BrowserHash), []byte(hashState(browserKey))) != 1 {
		return SignIn{}, ErrStateInvalid
	}
	access, err := s.provider.UserAccess(ctx, code, s.options.CallbackURL)
	if err != nil {
		return signIn, err
	}
	return signIn, s.store.ReplaceGrants(ctx, signIn.UserID, s.provider.Name(), access, s.now())
}

// Repositories is what the user's latest sign-in showed they may connect.
func (s *Service) Repositories(ctx context.Context, userID, projectID, siteID string) ([]gitprovider.Access, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.grantedTo(ctx, userID)
}

func (s *Service) grantedTo(ctx context.Context, userID string) ([]gitprovider.Access, error) {
	access, err := s.store.Grants(ctx, userID, s.provider.Name(), s.now().Add(-grantLifetime))
	if err != nil {
		return nil, err
	}
	if access == nil {
		access = []gitprovider.Access{}
	}
	return access, nil
}

// ConnectRequest connects a repository, named by id or by owner/name, to a site.
type ConnectRequest struct {
	ProjectID    string
	SiteID       string
	UserID       string
	RepositoryID int64
	Repository   string
	Settings     Settings
}

// ConnectResult is the saved connection and the first build it queued.
type ConnectResult struct {
	Connection *Connection `json:"connection"`
	Build      *Build      `json:"build,omitempty"`
	// BuildError says why no first build was queued; the connection is saved regardless.
	BuildError string `json:"buildError,omitempty"`
}

// Connect checks the user's own sign-in shows them the repository, that the
// App still reaches it, saves the connection, and queues a first build.
func (s *Service) Connect(ctx context.Context, request ConnectRequest) (*ConnectResult, error) {
	if err := s.mustSite(ctx, request.ProjectID, request.SiteID); err != nil {
		return nil, err
	}
	if err := s.mayConnect(ctx, request.UserID, request.ProjectID); err != nil {
		return nil, err
	}
	installation, repository, err := s.resolveRepository(ctx, request.UserID, request.RepositoryID, request.Repository)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Settings.Branch) == "" {
		request.Settings.Branch = repository.DefaultBranch
	}
	settings, err := request.Settings.Normalise()
	if err != nil {
		return nil, err
	}
	connection := &Connection{SiteID: request.SiteID, GitSource: s.newSource(request.ProjectID, request.UserID, installation, repository), Settings: settings}
	if err := s.store.SaveConnection(ctx, connection); err != nil {
		return nil, err
	}
	result := &ConnectResult{Connection: connection}
	build, err := s.buildHead(ctx, connection, request.UserID)
	if err != nil {
		result.BuildError = userMessage(err)
	}
	result.Build = build
	return result, nil
}

func (s *Service) newSource(projectID, userID string, installation int64, repository gitprovider.Repository) GitSource {
	return GitSource{ProjectID: projectID, Provider: s.provider.Name(), InstallationID: installation, RepositoryID: repository.ID,
		Repository: repository.FullName, ConnectedBy: userID}
}

// grantFor finds the repository in what the user's sign-in showed within the hour.
func (s *Service) grantFor(ctx context.Context, userID string, repositoryID int64, fullName string) (int64, gitprovider.Repository, error) {
	access, err := s.store.Grants(ctx, userID, s.provider.Name(), s.now().Add(-grantLifetime))
	if err != nil {
		return 0, gitprovider.Repository{}, err
	}
	for _, installation := range access {
		for _, repository := range installation.Repositories {
			if repository.ID == repositoryID || (fullName != "" && strings.EqualFold(repository.FullName, fullName)) {
				return installation.Installation.ID, repository, nil
			}
		}
	}
	return 0, gitprovider.Repository{}, ErrNotAuthorized
}

func (s *Service) Connection(ctx context.Context, projectID, siteID string) (*Connection, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	connection, err := s.store.GetConnection(ctx, projectID, siteID)
	if err != nil || connection == nil {
		return connection, err
	}
	if err := s.checkConnector(ctx, connection); err != nil && !errors.Is(err, ErrConnectionInactive) {
		return nil, err
	}
	return connection, nil
}

// Disconnect forgets the repository and cancels the site's queued and running builds.
func (s *Service) Disconnect(ctx context.Context, projectID, siteID string) error {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return err
	}
	if err := s.store.DeleteConnection(ctx, projectID, siteID); err != nil {
		return err
	}
	return s.store.CancelBuilds(ctx, Target{Kind: KindSite, ProjectID: projectID, ID: siteID}, disconnectedReason)
}

// Rebuild queues a build of the connected branch's head.
func (s *Service) Rebuild(ctx context.Context, projectID, siteID, actor string) (*Build, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	connection, err := s.store.GetConnection(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, ErrNotConnected
	}
	return s.buildHead(ctx, connection, actor)
}

func (s *Service) buildHead(ctx context.Context, connection connected, actor string) (*Build, error) {
	head, err := s.headOf(ctx, connection)
	if err != nil {
		return nil, err
	}
	build, _, err := s.enqueue(ctx, connection, head, TriggerRebuild, "", actor)
	return build, err
}

const disconnectedReason = "the repository was disconnected"

// enqueue sizes the build by the plan and queues it. A site's push past the hourly
// deploys is recorded FAILED so it is not lost silently.
func (s *Service) enqueue(ctx context.Context, connection connected, commit gitprovider.Commit, trigger, delivery, actor string) (*Build, bool, error) {
	if delivery != "" {
		queued, err := s.store.DeliveryQueued(ctx, connection.target(), delivery)
		if err != nil || queued {
			return nil, false, err
		}
	}
	build, err := s.sizedBuild(ctx, connection)
	if err != nil {
		return nil, false, err
	}
	source := connection.source()
	build.ID, build.ProjectID, build.Status, build.Trigger = uuid.NewString(), source.ProjectID, StatusQueued, trigger
	build.CommitSHA, build.CommitMessage, build.DeliveryID, build.RequestedBy = commit.SHA, commit.Message, delivery, actor
	build.Provider, build.InstallationID, build.RepositoryID, build.Repository = source.Provider, source.InstallationID, source.RepositoryID, source.Repository
	build.CreatedAt = s.now().UTC()
	if err := s.admit(ctx, build); err != nil {
		return nil, false, err
	}
	created, err := s.store.Enqueue(ctx, build, buildsKept)
	if err != nil {
		return nil, false, err
	}
	if created && build.Status == StatusQueued {
		s.nudge()
	}
	return build, created, nil
}

// sizedBuild is the target's part of a new build: its settings and the plan's limits, fixed now.
func (s *Service) sizedBuild(ctx context.Context, connection connected) (*Build, error) {
	if app, isApp := connection.(*AppConnection); isApp {
		return s.sizedAppBuild(ctx, app)
	}
	site, isSite := connection.(*Connection)
	if !isSite {
		return nil, fmt.Errorf("no build is sized for a %s connection", connection.target().Kind)
	}
	limits, err := s.limits.BuildLimits(ctx, site.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read the plan's build limits: %w", err)
	}
	return &Build{Kind: KindSite, SiteID: site.SiteID, Settings: site.Settings, Limits: limits}, nil
}

// admit counts the build toward the project's deploys or app builds per hour; a push past it is kept as FAILED, never started.
// admit takes a site build's deploy slot; an app build's slot is taken inside Enqueue, past its delivery check.
func (s *Service) admit(ctx context.Context, build *Build) error {
	if build.Kind == KindApp {
		return nil
	}
	if err := s.sites.TakeDeploySlot(ctx, build.ProjectID); err != nil {
		return overCap(build, err)
	}
	return nil
}

// overCap applies a refused slot: a push is recorded FAILED so it is not lost silently; anything else is refused.
func overCap(build *Build, slotErr error) error {
	limited := errors.Is(slotErr, sites.ErrDeployRateLimited) || errors.Is(slotErr, ErrAppBuildRateLimited)
	if build.Trigger != TriggerPush || !limited {
		return slotErr
	}
	build.Status, build.FailureKind, build.FailureReason = StatusFailed, FailRateLimited, slotErr.Error()
	return nil
}

// appBuildWait is the refusal for an app build that must wait; none when it may queue now.
func appBuildWait(wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	return &AppBuildRateLimitError{RetryAfter: max(wait, time.Second)}
}

// HandleWebhook verifies a delivery and queues a build for every site tracking the pushed branch.
func (s *Service) HandleWebhook(ctx context.Context, header map[string][]string, body []byte) (int, error) {
	if err := s.Available(); err != nil {
		return 0, err
	}
	event, err := s.provider.ParseWebhook(header, body)
	if err != nil {
		return 0, err
	}
	switch {
	case event.Push != nil:
		return s.handlePush(ctx, event)
	case event.RemovedInstallation != 0:
		return s.store.ForgetInstallation(ctx, s.provider.Name(), event.RemovedInstallation, nil)
	case len(event.RemovedRepositories) > 0:
		return s.store.ForgetInstallation(ctx, s.provider.Name(), event.RemovedFrom, event.RemovedRepositories)
	}
	return 0, nil
}

func (s *Service) handlePush(ctx context.Context, event gitprovider.Event) (int, error) {
	push := event.Push
	if push.Deleted {
		return 0, nil
	}
	if event.DeliveryID == "" {
		return 0, errors.New("the push has no delivery id")
	}
	seen, err := s.store.DeliverySeen(ctx, s.provider.Name(), event.DeliveryID)
	if err != nil || seen {
		return 0, err
	}
	connections, err := s.pushTargets(ctx, push)
	if err != nil || len(connections) == 0 {
		return 0, err
	}
	// The delivery id is not signed: a replayed push is caught by the branch having moved past it.
	head, err := s.provider.BranchHead(ctx, push.InstallationID, gitprovider.Repository{ID: push.RepositoryID, FullName: push.Repository}, push.Branch)
	if err != nil {
		return 0, fmt.Errorf("read the head of %s: %w", push.Branch, err)
	}
	if head.SHA != push.Commit.SHA {
		log.Printf("git builds: push %s to %s is not the branch head any more; ignored", push.Commit.SHA, push.Repository)
		return 0, s.store.RecordDelivery(ctx, s.provider.Name(), event.DeliveryID, s.now())
	}
	queued, err := s.queuePush(ctx, connections, push, event.DeliveryID)
	if err != nil {
		return queued, err
	}
	return queued, s.store.RecordDelivery(ctx, s.provider.Name(), event.DeliveryID, s.now())
}

// pushTargets are the sites, and while app builds are on the apps, tracking the pushed branch.
func (s *Service) pushTargets(ctx context.Context, push *gitprovider.Push) ([]connected, error) {
	siteConnections, err := s.store.ConnectionsForPush(ctx, s.provider.Name(), push.InstallationID, push.RepositoryID, push.Branch)
	if err != nil {
		return nil, err
	}
	targets := make([]connected, 0, len(siteConnections))
	for _, connection := range siteConnections {
		targets = append(targets, connection)
	}
	if !s.appsOn(ctx) {
		return targets, nil
	}
	appConnections, err := s.store.AppConnectionsForPush(ctx, s.provider.Name(), push.InstallationID, push.RepositoryID, push.Branch)
	if err != nil {
		return nil, err
	}
	for _, connection := range appConnections {
		targets = append(targets, connection)
	}
	return targets, nil
}

func (s *Service) queuePush(ctx context.Context, connections []connected, push *gitprovider.Push, delivery string) (int, error) {
	queued := 0
	var failures []error
	for _, connection := range connections {
		if err := s.checkConnector(ctx, connection); errors.Is(err, ErrConnectionInactive) {
			continue
		} else if err != nil {
			failures = append(failures, err)
			continue
		}
		source := connection.source()
		source.Repository = push.Repository
		_, created, err := s.enqueue(ctx, connection, push.Commit, TriggerPush, delivery, source.ConnectedBy)
		if err != nil {
			target := connection.target()
			log.Printf("git builds: queue a push to %s for %s %s: %v", push.Repository, target.Kind, target.ID, err)
			failures = append(failures, err)
			continue
		}
		if created {
			queued++
		}
	}
	return queued, errors.Join(failures...)
}

func (s *Service) ListBuilds(ctx context.Context, projectID, siteID string) ([]*Build, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.store.ListBuilds(ctx, Target{Kind: KindSite, ProjectID: projectID, ID: siteID}, buildsListLimit)
}

func (s *Service) GetBuild(ctx context.Context, projectID, siteID, id string) (*Build, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.oneBuild(ctx, Target{Kind: KindSite, ProjectID: projectID, ID: siteID}, id)
}

func (s *Service) oneBuild(ctx context.Context, target Target, id string) (*Build, error) {
	build, err := s.store.GetBuild(ctx, target, id)
	if err != nil {
		return nil, err
	}
	if build == nil {
		return nil, ErrBuildNotFound
	}
	return build, nil
}

// SecretVisibility lists the project's secrets by name with whether builds see them; values never leave.
func (s *Service) SecretVisibility(ctx context.Context, projectID string) ([]SecretVisibility, error) {
	if err := s.Available(); err != nil {
		return nil, err
	}
	values, err := s.secrets.Secrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	visible, err := s.store.VisibleSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	marked := map[string]bool{}
	for _, name := range visible {
		marked[name] = true
	}
	out := []SecretVisibility{}
	for _, name := range sortedNames(values) {
		out = append(out, SecretVisibility{Name: name, BuildVisible: marked[name]})
	}
	return out, nil
}

// SetSecretVisibility makes exactly these secrets visible to the project's builds.
func (s *Service) SetSecretVisibility(ctx context.Context, projectID string, names []string, actor string) ([]SecretVisibility, error) {
	if err := s.Available(); err != nil {
		return nil, err
	}
	values, err := s.secrets.Secrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, held := values[name]; !held {
			return nil, &InputError{Reason: fmt.Sprintf("the project has no secret named %q", name)}
		}
	}
	if err := s.store.SetVisibleSecrets(ctx, projectID, names, actor); err != nil {
		return nil, err
	}
	return s.SecretVisibility(ctx, projectID)
}

// userMessage is an error as a person reading the build history may see it.
func userMessage(err error) string {
	var input *InputError
	var archive *sites.ArchiveError
	var siteInput *sites.InputError
	var publish *sites.PublishError
	var limited *sites.RateLimitError
	switch {
	case errors.As(err, &input), errors.As(err, &archive), errors.As(err, &siteInput), errors.As(err, &publish), errors.As(err, &limited),
		errors.Is(err, sites.ErrDeployRateLimited), errors.Is(err, ErrOutputTooLarge), errors.Is(err, sites.ErrSiteNotFound),
		errors.Is(err, ErrConnectionInactive), errors.Is(err, apphost.ErrAppNotFound), errors.Is(err, ErrAppBuildsOff), errors.Is(err, ErrAppBuildRateLimited),
		errors.Is(err, errAppsUnwired):
		return err.Error()
	}
	log.Printf("git builds: %v", err)
	return "an internal error stopped this; try again"
}

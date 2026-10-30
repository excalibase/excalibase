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
	// Previews deploys branch and pull request previews (EXC-660); nil leaves them off.
	Previews PreviewSites
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

const connectorGoneReason = "the member who connected this repository can no longer deploy this project"

// checkConnector deactivates a connection whose connector lost the project or the developer rung,
// and cancels the site's waiting builds; a repository is only ever read under its connector's sign-in.
func (s *Service) checkConnector(ctx context.Context, connection *Connection) error {
	if connection.InactiveReason != "" {
		return ErrConnectionInactive
	}
	if s.options.Members == nil {
		return nil
	}
	standing, err := s.options.Members.Standing(ctx, connection.ConnectedBy, connection.ProjectID)
	if err != nil {
		return fmt.Errorf("read the connector's role: %w", err)
	}
	connection.ConnectedByName = standing.Name
	if standing.CanDeploy {
		return nil
	}
	if err := s.store.DeactivateConnection(ctx, connection.SiteID, connectorGoneReason); err != nil {
		return err
	}
	if err := s.store.CancelSiteBuilds(ctx, connection.SiteID, connectorGoneReason); err != nil {
		return err
	}
	connection.InactiveReason = connectorGoneReason
	return ErrConnectionInactive
}

// currentRepository reads the repository's owner/name by its id, so a transfer or rename is followed
// and a stale name someone else now owns is never cloned.
func (s *Service) currentRepository(ctx context.Context, connection *Connection) error {
	repository, err := s.provider.Repository(ctx, connection.InstallationID, connection.RepositoryID)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return &InputError{Reason: "the GitHub App no longer covers " + connection.Repository}
	}
	if err != nil {
		return fmt.Errorf("read the repository: %w", err)
	}
	connection.private = repository.Private
	if repository.FullName == connection.Repository {
		return nil
	}
	if err := s.store.RenameRepository(ctx, connection.SiteID, connection.RepositoryID, repository.FullName); err != nil {
		return err
	}
	connection.Repository = repository.FullName
	return nil
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
	state, err := randomToken()
	if err != nil {
		return StartedSignIn{}, err
	}
	browserKey, err := randomToken()
	if err != nil {
		return StartedSignIn{}, err
	}
	signIn := SignIn{UserID: userID, ProjectID: projectID, SiteID: siteID, BrowserHash: hashState(browserKey)}
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
	installation, granted, err := s.grantFor(ctx, request)
	if err != nil {
		return nil, err
	}
	repository, err := s.provider.Repository(ctx, installation, granted.ID)
	if err != nil {
		if errors.Is(err, gitprovider.ErrNotFound) {
			return nil, &InputError{Reason: "the GitHub App no longer covers " + granted.FullName + "; add it to the installation on GitHub"}
		}
		return nil, fmt.Errorf("read the repository: %w", err)
	}
	if strings.TrimSpace(request.Settings.Branch) == "" {
		request.Settings.Branch = repository.DefaultBranch
	}
	settings, err := request.Settings.Normalise()
	if err != nil {
		return nil, err
	}
	connection := &Connection{
		SiteID: request.SiteID, ProjectID: request.ProjectID, Provider: s.provider.Name(), InstallationID: installation,
		RepositoryID: repository.ID, Repository: repository.FullName, Settings: settings, ConnectedBy: request.UserID,
	}
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

// grantFor finds the repository in what the user's sign-in showed within the hour.
func (s *Service) grantFor(ctx context.Context, request ConnectRequest) (int64, gitprovider.Repository, error) {
	access, err := s.store.Grants(ctx, request.UserID, s.provider.Name(), s.now().Add(-grantLifetime))
	if err != nil {
		return 0, gitprovider.Repository{}, err
	}
	for _, installation := range access {
		for _, repository := range installation.Repositories {
			if repository.ID == request.RepositoryID || (request.Repository != "" && strings.EqualFold(repository.FullName, request.Repository)) {
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
	if err := s.removeSitePreviews(ctx, projectID, siteID); err != nil {
		return err
	}
	if err := s.store.DeleteConnection(ctx, projectID, siteID); err != nil {
		return err
	}
	return s.store.CancelSiteBuilds(ctx, siteID, "the repository was disconnected")
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

func (s *Service) buildHead(ctx context.Context, connection *Connection, actor string) (*Build, error) {
	if err := s.checkConnector(ctx, connection); err != nil {
		return nil, err
	}
	if err := s.currentRepository(ctx, connection); err != nil {
		return nil, err
	}
	repository := gitprovider.Repository{ID: connection.RepositoryID, FullName: connection.Repository}
	head, err := s.provider.BranchHead(ctx, connection.InstallationID, repository, connection.Settings.Branch)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return nil, &InputError{Reason: fmt.Sprintf("branch %s was not found in %s", connection.Settings.Branch, connection.Repository)}
	}
	if err != nil {
		return nil, fmt.Errorf("read the branch head: %w", err)
	}
	build, _, err := s.enqueue(ctx, connection, head, TriggerRebuild, "", actor, nil)
	return build, err
}

// enqueue sizes the build by the plan, counts it toward the hourly deploys and
// queues it. A push past the hourly limit is recorded FAILED so it is not lost silently.
// shape, when given, fills in what a preview build adds before anything is counted.
func (s *Service) enqueue(ctx context.Context, connection *Connection, commit gitprovider.Commit, trigger, delivery, actor string,
	shape func(*Build)) (*Build, bool, error) {
	limits, err := s.limits.BuildLimits(ctx, connection.ProjectID)
	if err != nil {
		return nil, false, fmt.Errorf("read the plan's build limits: %w", err)
	}
	build := &Build{
		ID: uuid.NewString(), SiteID: connection.SiteID, ProjectID: connection.ProjectID, Status: StatusQueued, Trigger: trigger,
		CommitSHA: commit.SHA, CommitMessage: commit.Message, Provider: connection.Provider, InstallationID: connection.InstallationID,
		RepositoryID: connection.RepositoryID, Repository: connection.Repository, Settings: connection.Settings, Limits: limits,
		DeliveryID: delivery, RequestedBy: actor, CreatedAt: s.now().UTC(),
	}
	if shape != nil {
		shape(build)
	}
	if delivery != "" {
		queued, err := s.store.DeliveryQueued(ctx, connection.SiteID, delivery)
		if err != nil || queued {
			return nil, false, err
		}
	}
	if build.Status != StatusQueued {
		created, err := s.store.Enqueue(ctx, build, buildsKept)
		return build, created, err
	}
	slotErr := s.takeSlot(ctx, connection.ProjectID, trigger)
	if slotErr != nil && trigger == TriggerRebuild {
		return nil, false, slotErr
	}
	if slotErr != nil && !errors.Is(slotErr, sites.ErrDeployRateLimited) {
		return nil, false, slotErr
	}
	if slotErr != nil {
		build.Status, build.FailureKind, build.FailureReason = StatusFailed, FailRateLimited, slotErr.Error()
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

// takeSlot counts a preview build against the previews' own budget, any other against the project's deploys.
func (s *Service) takeSlot(ctx context.Context, projectID, trigger string) error {
	if trigger == TriggerPreview && s.options.Previews != nil {
		return s.options.Previews.TakePreviewSlot(ctx, projectID)
	}
	return s.sites.TakeDeploySlot(ctx, projectID)
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
	case event.PullRequest != nil:
		if s.options.Previews == nil {
			return 0, nil
		}
		return s.handlePullRequest(ctx, event)
	case event.RemovedInstallation != 0:
		return s.forgetInstallation(ctx, event.RemovedInstallation, nil)
	case len(event.RemovedRepositories) > 0:
		return s.forgetInstallation(ctx, event.RemovedFrom, event.RemovedRepositories)
	}
	return 0, nil
}

func (s *Service) handlePush(ctx context.Context, event gitprovider.Event) (int, error) {
	push := event.Push
	if push.Deleted {
		return s.removeBranchPreviews(ctx, push)
	}
	if event.DeliveryID == "" {
		return 0, errors.New("the push has no delivery id")
	}
	seen, err := s.store.DeliverySeen(ctx, s.provider.Name(), event.DeliveryID)
	if err != nil || seen {
		return 0, err
	}
	connections, err := s.store.ConnectionsForPush(ctx, s.provider.Name(), push.InstallationID, push.RepositoryID, push.Branch)
	if err != nil {
		return 0, err
	}
	previewed, err := s.branchPreviews(ctx, push)
	if err != nil || len(connections)+len(previewed) == 0 {
		return 0, err
	}
	// The delivery id is not signed: a replayed push is caught by the branch having moved past it.
	head, err := s.provider.BranchHead(ctx, push.InstallationID, gitprovider.Repository{ID: push.RepositoryID, FullName: push.Repository}, push.Branch)
	if err != nil {
		return 0, fmt.Errorf("read the head of %s: %w", push.Branch, err)
	}
	if head.SHA != push.Commit.SHA {
		log.Printf("site builds: push %s to %s is not the branch head any more; ignored", push.Commit.SHA, push.Repository)
		return 0, s.store.RecordDelivery(ctx, s.provider.Name(), event.DeliveryID, s.now())
	}
	queued := 0
	var failures []error
	for _, connection := range connections {
		if err := s.checkConnector(ctx, connection); errors.Is(err, ErrConnectionInactive) {
			continue
		} else if err != nil {
			failures = append(failures, err)
			continue
		}
		connection.Repository = push.Repository
		_, created, err := s.enqueue(ctx, connection, push.Commit, TriggerPush, event.DeliveryID, connection.ConnectedBy, nil)
		if err != nil {
			log.Printf("site builds: queue a push to %s for site %s: %v", push.Repository, connection.SiteID, err)
			failures = append(failures, err)
			continue
		}
		if created {
			queued++
		}
	}
	for _, connection := range previewed {
		connection.Repository = push.Repository
		created, err := s.queuePreview(ctx, connection, push.Commit, previewTarget{source: sites.PreviewSourceBranch, ref: push.Branch}, event.DeliveryID)
		if err != nil {
			log.Printf("site builds: queue a preview of %s for site %s: %v", push.Branch, connection.SiteID, err)
			failures = append(failures, err)
			continue
		}
		if created {
			queued++
		}
	}
	if len(failures) > 0 {
		return queued, errors.Join(failures...)
	}
	return queued, s.store.RecordDelivery(ctx, s.provider.Name(), event.DeliveryID, s.now())
}

func (s *Service) ListBuilds(ctx context.Context, projectID, siteID string) ([]*Build, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.store.ListBuilds(ctx, projectID, siteID, buildsListLimit)
}

func (s *Service) GetBuild(ctx context.Context, projectID, siteID, id string) (*Build, error) {
	if err := s.mustSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	build, err := s.store.GetBuild(ctx, projectID, siteID, id)
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
		errors.Is(err, ErrConnectionInactive):
		return err.Error()
	}
	log.Printf("site builds: %v", err)
	return "an internal error stopped this; try again"
}

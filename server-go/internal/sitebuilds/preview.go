package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// Preview modes (EXC-660): which pushes and pull requests of a connected repository get a preview.
const (
	PreviewModeOff          = "off"
	PreviewModePullRequests = "pull-requests"
	// PreviewModeBranches previews every branch but the production one, and fork pull requests when allowed.
	PreviewModeBranches = "branches"
)

// PreviewsRunningPerProject is how many preview builds of one project run at once, apart from its
// production builds; the rest wait in the queue (EXC-660).
const PreviewsRunningPerProject = 1

const (
	// statusTimeout bounds posting a commit status; it is a courtesy, never a reason to fail.
	statusTimeout = 30 * time.Second
	statusContext = "excalibase/preview"
)

// ErrPreviewsOff is an install where previews are not switched on; routes answer it as unmounted.
var ErrPreviewsOff = errors.New("previews are not available on this install")

// PreviewAccess are the opt-ins only an Admin turns on; each reaches past the preview itself.
type PreviewAccess struct {
	// Forks builds pull requests from forks; their builds never see a project secret.
	Forks bool `json:"forks"`
	// Secrets gives same-repository preview builds the build-visible project secrets.
	Secrets bool `json:"secrets"`
	// APIAccess adds each preview's origin to the project's CORS list.
	APIAccess bool `json:"apiAccess"`
	// PublicLinks posts the preview link on public repositories too.
	PublicLinks bool `json:"publicLinks"`
}

// PreviewSettings are a connection's previews: the mode a developer sets and the Admin's opt-ins.
type PreviewSettings struct {
	Mode string `json:"mode"`
	PreviewAccess
}

func (p PreviewSettings) normalised() PreviewSettings {
	if p.Mode == "" {
		p.Mode = PreviewModeOff
	}
	return p
}

func validPreviewMode(mode string) error {
	switch mode {
	case PreviewModeOff, PreviewModePullRequests, PreviewModeBranches:
		return nil
	}
	return &InputError{Reason: "previews are off, pull-requests or branches"}
}

// PreviewBuild is what a preview build keeps from its trigger and the opt-ins when it was queued.
type PreviewBuild struct {
	Source    string `json:"source"`
	Ref       string `json:"ref"`
	FromFork  bool   `json:"fromFork"`
	Secrets   bool   `json:"secrets"`
	APIAccess bool   `json:"apiAccess"`
	// PublicLinks and privateRepo decide whether the status carries the link.
	PublicLinks bool `json:"publicLinks"`
	privateRepo bool
}

// seesSecrets: a fork's build never does, whatever the opt-in.
func (p *PreviewBuild) seesSecrets() bool { return p.Secrets && !p.FromFork }

// narrow keeps only the opt-ins the connection still grants, so an Admin's withdrawal reaches a
// build already queued; it answers why the build may not go on at all, or "".
func (p *PreviewBuild) narrow(current PreviewSettings) string {
	if !previewAllowed(current, p.Source, p.FromFork) {
		return "previews of this kind were turned off while this build waited; nothing was deployed"
	}
	p.Secrets = p.Secrets && current.Secrets
	p.APIAccess = p.APIAccess && current.APIAccess
	p.PublicLinks = p.PublicLinks && current.PublicLinks
	return ""
}

// PreviewSites is the Sites service previews deploy through; nil leaves previews off.
type PreviewSites interface {
	Get(ctx context.Context, projectID, id string) (*sites.Site, error)
	OpenPreview(ctx context.Context, request sites.PreviewRequest) (*sites.Preview, error)
	DeployPreview(ctx context.Context, request sites.PreviewDeployRequest) (*sites.Preview, error)
	RecordPreviewFailure(ctx context.Context, projectID, siteID, previewID, reason string) error
	RemovePreview(ctx context.Context, projectID, siteID, previewID string) error
	RemovePreviewByRef(ctx context.Context, projectID, siteID, source, ref string) error
	ListPreviews(ctx context.Context, projectID, siteID string) ([]*sites.Preview, error)
	PreviewUsage(ctx context.Context, projectID string) (int, int, error)
	WithdrawPreviewAPIAccess(ctx context.Context, projectID, siteID string) error
	TakePreviewSlot(ctx context.Context, projectID string) error
	// PreviewsAvailable is false while previews are dark: none is made, the held ones are still removed.
	PreviewsAvailable() bool
}

// PreviewsAvailable answers why previews cannot be used here, or nil.
func (s *Service) PreviewsAvailable() error {
	if err := s.Available(); err != nil {
		return err
	}
	if !s.makesPreviews() {
		return ErrPreviewsOff
	}
	return nil
}

// makesPreviews is whether new previews are made; removal only needs options.Previews.
func (s *Service) makesPreviews() bool {
	return s.options.Previews != nil && s.options.Previews.PreviewsAvailable()
}

// previewAllowed is whether settings still make this kind of preview: a mode change or forks
// turned off removes the previews they no longer cover.
func previewAllowed(settings PreviewSettings, source string, fromFork bool) bool {
	if fromFork && !settings.Forks {
		return false
	}
	switch settings.normalised().Mode {
	case PreviewModePullRequests:
		return source == sites.PreviewSourcePullRequest
	case PreviewModeBranches:
		// A same-repository pull request is previewed as its branch in this mode.
		return source == sites.PreviewSourceBranch || fromFork
	}
	return false
}

// PreviewOverview is a site's preview settings, its previews and the project's use of its plan.
type PreviewOverview struct {
	Settings PreviewSettings  `json:"settings"`
	Previews []*sites.Preview `json:"previews"`
	Used     int              `json:"used"`
	Max      int              `json:"max"`
}

func (s *Service) mustPreviewSite(ctx context.Context, projectID, siteID string) error {
	if err := s.PreviewsAvailable(); err != nil {
		return err
	}
	_, err := s.sites.Get(ctx, projectID, siteID)
	return err
}

func (s *Service) Previews(ctx context.Context, projectID, siteID string) (*PreviewOverview, error) {
	if err := s.mustPreviewSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	overview := &PreviewOverview{Settings: PreviewSettings{Mode: PreviewModeOff}}
	connection, err := s.store.GetConnection(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	if connection != nil {
		overview.Settings = connection.Previews.normalised()
	}
	if overview.Previews, err = s.options.Previews.ListPreviews(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	overview.Used, overview.Max, err = s.options.Previews.PreviewUsage(ctx, projectID)
	return overview, err
}

// SetPreviewMode chooses what gets previews; turning them off removes the site's previews.
func (s *Service) SetPreviewMode(ctx context.Context, projectID, siteID, mode string) (*PreviewOverview, error) {
	if err := s.mustPreviewSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	if err := validPreviewMode(mode); err != nil {
		return nil, err
	}
	return s.updatePreviews(ctx, projectID, siteID, func() error { return s.store.SavePreviewMode(ctx, projectID, siteID, mode) })
}

// SetPreviewAccess records the Admin's opt-ins; the next preview build snapshots them.
func (s *Service) SetPreviewAccess(ctx context.Context, projectID, siteID string, access PreviewAccess) (*PreviewOverview, error) {
	if err := s.mustPreviewSite(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.updatePreviews(ctx, projectID, siteID, func() error { return s.store.SavePreviewAccess(ctx, projectID, siteID, access) })
}

// updatePreviews saves one change, then takes down what the settings now stored no longer allow:
// previews of another mode, fork previews once forks are off, and preview origins once API access is.
func (s *Service) updatePreviews(ctx context.Context, projectID, siteID string, save func() error) (*PreviewOverview, error) {
	if err := save(); err != nil {
		return nil, err
	}
	connection, err := s.store.GetConnection(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, ErrNotConnected
	}
	if err := s.pruneSitePreviews(ctx, projectID, siteID, connection.Previews.normalised()); err != nil {
		return nil, err
	}
	return s.Previews(ctx, projectID, siteID)
}

func (s *Service) pruneSitePreviews(ctx context.Context, projectID, siteID string, settings PreviewSettings) error {
	held, err := s.options.Previews.ListPreviews(ctx, projectID, siteID)
	if err != nil {
		return err
	}
	var failures []error
	for _, preview := range held {
		if !previewAllowed(settings, preview.Source, preview.FromFork) {
			failures = append(failures, s.options.Previews.RemovePreview(ctx, projectID, siteID, preview.ID))
		}
	}
	if !settings.APIAccess {
		failures = append(failures, s.options.Previews.WithdrawPreviewAPIAccess(ctx, projectID, siteID))
	}
	return errors.Join(failures...)
}

// RemovePreview takes one preview down now, as closing its pull request would.
func (s *Service) RemovePreview(ctx context.Context, projectID, siteID, previewID string) error {
	if err := s.mustPreviewSite(ctx, projectID, siteID); err != nil {
		return err
	}
	return s.options.Previews.RemovePreview(ctx, projectID, siteID, previewID)
}

func (s *Service) removeSitePreviews(ctx context.Context, projectID, siteID string) error {
	if s.options.Previews == nil {
		return nil
	}
	held, err := s.options.Previews.ListPreviews(ctx, projectID, siteID)
	if err != nil {
		return err
	}
	var failures []error
	for _, preview := range held {
		if err := s.options.Previews.RemovePreview(ctx, projectID, siteID, preview.ID); err != nil {
			failures = append(failures, fmt.Errorf("remove preview %s: %w", preview.Hostname, err))
		}
	}
	return errors.Join(failures...)
}

// forgetInstallation drops the connections the App no longer covers, taking their previews down
// first; a preview that could not be removed is left REMOVING for the sweep, never forgotten.
func (s *Service) forgetInstallation(ctx context.Context, installationID int64, repositoryIDs []int64) (int, error) {
	if s.options.Previews != nil {
		connections, err := s.store.ConnectionsForInstallation(ctx, s.provider.Name(), installationID, repositoryIDs)
		if err != nil {
			return 0, err
		}
		for _, connection := range connections {
			if err := s.removeSitePreviews(ctx, connection.ProjectID, connection.SiteID); err != nil {
				log.Printf("site builds: remove the previews of site %s on uninstall: %v", connection.SiteID, err)
			}
		}
	}
	return s.store.ForgetInstallation(ctx, s.provider.Name(), installationID, repositoryIDs)
}

// previewConnections are the repository's connections with previews on; none while previews are off.
func (s *Service) previewConnections(ctx context.Context, installationID, repositoryID int64) ([]*Connection, error) {
	if s.options.Previews == nil {
		return nil, nil
	}
	all, err := s.store.ConnectionsForRepository(ctx, s.provider.Name(), installationID, repositoryID)
	if err != nil {
		return nil, err
	}
	var on []*Connection
	for _, connection := range all {
		if connection.Previews.normalised().Mode != PreviewModeOff {
			on = append(on, connection)
		}
	}
	return on, nil
}

// branchPreviews are the connections a push to a non-production branch previews.
func (s *Service) branchPreviews(ctx context.Context, push *gitprovider.Push) ([]*Connection, error) {
	if validBranch(push.Branch) != nil || !s.makesPreviews() {
		return nil, nil
	}
	connections, err := s.previewConnections(ctx, push.InstallationID, push.RepositoryID)
	if err != nil {
		return nil, err
	}
	var targets []*Connection
	for _, connection := range connections {
		if connection.Previews.Mode == PreviewModeBranches && connection.Settings.Branch != push.Branch {
			targets = append(targets, connection)
		}
	}
	return targets, nil
}

// removeBranchPreviews acts on a deleted branch only once the host no longer shows it, so a replay removes nothing.
func (s *Service) removeBranchPreviews(ctx context.Context, push *gitprovider.Push) (int, error) {
	connections, err := s.previewConnections(ctx, push.InstallationID, push.RepositoryID)
	if err != nil || len(connections) == 0 {
		return 0, err
	}
	repository := gitprovider.Repository{ID: push.RepositoryID, FullName: push.Repository}
	_, err = s.provider.BranchHead(ctx, push.InstallationID, repository, push.Branch)
	if err == nil {
		log.Printf("site builds: branch %s of %s still exists; its delete event is ignored", push.Branch, push.Repository)
		return 0, nil
	}
	if !errors.Is(err, gitprovider.ErrNotFound) {
		return 0, fmt.Errorf("read the branch %s: %w", push.Branch, err)
	}
	var failures []error
	for _, connection := range connections {
		failures = append(failures, s.options.Previews.RemovePreviewByRef(ctx, connection.ProjectID, connection.SiteID, sites.PreviewSourceBranch, push.Branch))
	}
	return 0, errors.Join(failures...)
}

// handlePullRequest previews a pull request, or removes its preview once it reads closed.
func (s *Service) handlePullRequest(ctx context.Context, event gitprovider.Event) (int, error) {
	pr := event.PullRequest
	if event.DeliveryID == "" {
		return 0, errors.New("the pull request event has no delivery id")
	}
	seen, err := s.store.DeliverySeen(ctx, s.provider.Name(), event.DeliveryID)
	if err != nil || seen {
		return 0, err
	}
	connections, err := s.previewConnections(ctx, pr.InstallationID, pr.RepositoryID)
	if err != nil || len(connections) == 0 {
		return 0, err
	}
	repository := gitprovider.Repository{ID: pr.RepositoryID, FullName: pr.Repository}
	// The delivery id is not signed: a replay is caught by the pull request having moved on.
	state, err := s.provider.PullRequest(ctx, pr.InstallationID, repository, pr.Number)
	if err != nil {
		return 0, fmt.Errorf("read pull request #%d of %s: %w", pr.Number, pr.Repository, err)
	}
	queued, err := s.actOnPullRequest(ctx, event, state, connections)
	if err != nil {
		return queued, err
	}
	return queued, s.store.RecordDelivery(ctx, s.provider.Name(), event.DeliveryID, s.now())
}

func (s *Service) actOnPullRequest(ctx context.Context, event gitprovider.Event, state gitprovider.PullRequestState, connections []*Connection) (int, error) {
	pr, ref := event.PullRequest, strconv.Itoa(event.PullRequest.Number)
	if pr.Action == gitprovider.PullRequestClosed {
		if state.Open {
			return 0, nil
		}
		var failures []error
		for _, connection := range connections {
			failures = append(failures, s.options.Previews.RemovePreviewByRef(ctx, connection.ProjectID, connection.SiteID, sites.PreviewSourcePullRequest, ref))
		}
		return 0, errors.Join(failures...)
	}
	if !s.makesPreviews() {
		return 0, nil
	}
	if !state.Open || state.HeadSHA != pr.Commit.SHA {
		log.Printf("site builds: pull request #%d of %s is not open at %s any more; ignored", pr.Number, pr.Repository, pr.Commit.SHA)
		return 0, nil
	}
	queued := 0
	var failures []error
	for _, connection := range connections {
		if !previewAllowed(connection.Previews, sites.PreviewSourcePullRequest, state.FromFork) {
			continue
		}
		target := previewTarget{source: sites.PreviewSourcePullRequest, ref: ref, fromFork: state.FromFork}
		created, err := s.queuePreview(ctx, connection, pr.Commit, target, event.DeliveryID)
		if err != nil {
			failures = append(failures, err)
		} else if created {
			queued++
		}
	}
	return queued, errors.Join(failures...)
}

// previewTarget is what a preview is of.
type previewTarget struct {
	source   string
	ref      string
	fromFork bool
}

// queuePreview opens the preview within the caps and queues its build; a refusal is recorded as a failed build.
func (s *Service) queuePreview(ctx context.Context, connection *Connection, commit gitprovider.Commit, target previewTarget, delivery string) (bool, error) {
	if err := s.checkConnector(ctx, connection); errors.Is(err, ErrConnectionInactive) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	settings := connection.Previews.normalised()
	snapshot := &PreviewBuild{Source: target.source, Ref: target.ref, FromFork: target.fromFork, Secrets: settings.Secrets,
		APIAccess: settings.APIAccess, PublicLinks: settings.PublicLinks}
	preview, openErr := s.options.Previews.OpenPreview(ctx, sites.PreviewRequest{ProjectID: connection.ProjectID, SiteID: connection.SiteID,
		Source: target.source, Ref: target.ref, CommitSHA: commit.SHA, Actor: connection.ConnectedBy, FromFork: target.fromFork})
	if openErr != nil && !refusedPreview(openErr) {
		return false, openErr
	}
	build, created, err := s.enqueue(ctx, connection, commit, TriggerPreview, delivery, connection.ConnectedBy, func(build *Build) {
		build.Preview = snapshot
		if preview != nil {
			build.PreviewID = preview.ID
			return
		}
		build.Status, build.FailureKind, build.FailureReason = StatusFailed, FailPreviewLimit, openErr.Error()
	})
	if err != nil && preview != nil {
		// Nothing was queued for the preview it just marked building.
		if recordErr := s.options.Previews.RecordPreviewFailure(ctx, connection.ProjectID, connection.SiteID, preview.ID, userMessage(err)); recordErr != nil {
			log.Printf("site builds: record the unqueued preview %s: %v", preview.ID, recordErr)
		}
	}
	if err == nil && created && build.Status == StatusFailed {
		if build.PreviewID != "" {
			if recordErr := s.options.Previews.RecordPreviewFailure(ctx, build.ProjectID, build.SiteID, build.PreviewID, build.FailureReason); recordErr != nil {
				log.Printf("site build %s: record the refused preview: %v", build.ID, recordErr)
			}
		}
		s.postStatus(ctx, build, gitprovider.StatusFailure, "", build.FailureReason)
	}
	return created, err
}

func refusedPreview(err error) bool {
	return errors.Is(err, sites.ErrPreviewLimitReached) || errors.Is(err, sites.ErrPreviewInstallFull) || errors.Is(err, sites.ErrPreviewRemoving)
}

// postStatus puts the preview's outcome on its commit; failing to is logged, never a build failure.
func (s *Service) postStatus(ctx context.Context, build *Build, state, link, description string) {
	if build.Preview == nil {
		return
	}
	site, err := s.sites.Get(ctx, build.ProjectID, build.SiteID)
	if err != nil {
		log.Printf("site build %s: read the site for its status: %v", build.ID, err)
		return
	}
	if !build.Preview.privateRepo && !build.Preview.PublicLinks {
		link = ""
	}
	if runes := []rune(description); len(runes) > maxStatusDescription {
		description = string(runes[:maxStatusDescription-1]) + "…"
	}
	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer cancel()
	repository := gitprovider.Repository{ID: build.RepositoryID, FullName: build.Repository}
	status := gitprovider.CommitStatus{State: state, TargetURL: link, Description: description, Context: statusContext + ": " + site.Name}
	if err := s.provider.SetCommitStatus(statusCtx, build.InstallationID, repository, build.CommitSHA, status); err != nil {
		log.Printf("site build %s: post the preview status: %v", build.ID, err)
	}
}

// maxStatusDescription is GitHub's limit on a status description.
const maxStatusDescription = 140

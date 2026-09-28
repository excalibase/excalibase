package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/google/uuid"
)

// ErrDomainNotPointed refuses a domain whose CNAME does not name the app's own hostname.
var ErrDomainNotPointed = errors.New("the domain's CNAME does not point at the app")

// ErrDomainOnInternalService refuses a custom domain for an app with no public route (EXC-525).
var ErrDomainOnInternalService = errors.New("an internal service has no public route, so it cannot serve a custom domain")

// OperationDomain names the lease a custom-domain change takes on its app.
const OperationDomain ProjectOperation = "custom domain change"

type CNAMEResolver interface {
	CNAME(ctx context.Context, host string) (string, error)
}

// DomainView is a domain as the API shows it, with the CNAME target it must point at.
type DomainView struct {
	apphost.Domain
	CNAMETarget string `json:"cnameTarget"`
}

// AppDomainService attaches customers' own domains to their apps. A domain is
// routed only once its CNAME is seen pointing at the app's own hostname, and
// that is checked again daily.
type AppDomainService struct {
	apps      apphost.Store
	domains   apphost.DomainStore
	kube      k8s.KubeClient
	instances storage.InstanceStore
	dns       CNAMEResolver
	leases    *AppDeployService
	route     apphost.Route
	opts      k8s.AppDomainOptions
	now       func() time.Time
}

func NewAppDomainService(apps apphost.Store, domains apphost.DomainStore, kube k8s.KubeClient, instances storage.InstanceStore,
	dns CNAMEResolver, leases *AppDeployService, route apphost.Route, opts k8s.AppDomainOptions) *AppDomainService {
	return &AppDomainService{apps: apps, domains: domains, kube: kube, instances: instances, dns: dns,
		leases: leases, route: route, opts: opts, now: func() time.Time { return time.Now().UTC() }}
}

func (s *AppDomainService) Add(ctx context.Context, projectID, appID, hostname string) (*DomainView, error) {
	app, err := s.leases.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	if app.Internal {
		return nil, ErrDomainOnInternalService
	}
	host, err := apphost.NormalizeCustomDomain(hostname, s.route.Domain)
	if err != nil {
		return nil, err
	}
	domain := &apphost.Domain{ID: uuid.NewString(), ProjectID: projectID, AppID: appID, Hostname: host,
		Status: apphost.DomainPending, CreatedAt: s.now()}
	if err := s.domains.Add(domain); err != nil {
		return nil, err
	}
	return s.view(app, domain)
}

func (s *AppDomainService) List(projectID, appID string) ([]*DomainView, error) {
	app, err := s.leases.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	domains, err := s.domains.List(projectID, appID)
	if err != nil {
		return nil, err
	}
	views := make([]*DomainView, 0, len(domains))
	for _, domain := range domains {
		view, err := s.view(app, domain)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *AppDomainService) view(app *apphost.App, domain *apphost.Domain) (*DomainView, error) {
	target, err := s.route.Hostname(app.Name, app.ProjectID)
	if err != nil {
		return nil, err
	}
	return &DomainView{Domain: *domain, CNAMETarget: target}, nil
}

// Verify routes the domain once its CNAME names the app's own hostname. A
// pending claim holds nothing, so only this proof decides who gets a hostname.
func (s *AppDomainService) Verify(ctx context.Context, projectID, appID, id string) (*DomainView, error) {
	release, err := s.leases.holdAppForDeploy(ctx, projectID, appID)
	if err != nil {
		return nil, err
	}
	defer release()
	app, domain, err := s.lookup(projectID, appID, id)
	if err != nil {
		return nil, err
	}
	if apphost.DomainRoutable(domain.Status) {
		return s.view(app, domain)
	}
	if err := s.proveOwnership(ctx, app, domain); err != nil {
		return nil, err
	}
	if err := s.domains.Verify(projectID, appID, id, s.now()); err != nil {
		return nil, err
	}
	if err := s.SyncApp(ctx, "", app); err != nil {
		return nil, errors.Join(err, s.domains.SetStatus(id, apphost.DomainPending, "the route could not be written; verify again", 0, s.now()))
	}
	domain, err = s.domains.Get(projectID, appID, id)
	if err != nil || domain == nil {
		return nil, fmt.Errorf("read domain: %w", errors.Join(err, apphost.ErrDomainNotFound))
	}
	return s.view(app, domain)
}

// proveOwnership records why a domain is not yet proven; a lookup that failed says nothing either way.
func (s *AppDomainService) proveOwnership(ctx context.Context, app *apphost.App, domain *apphost.Domain) error {
	target, err := s.route.Hostname(app.Name, app.ProjectID)
	if err != nil {
		return err
	}
	got, err := s.dns.CNAME(ctx, domain.Hostname)
	if err != nil {
		return fmt.Errorf("look up the CNAME of %s: %w", domain.Hostname, err)
	}
	if got == target {
		return nil
	}
	reason := fmt.Sprintf("%s has no CNAME; add one pointing at %s", domain.Hostname, target)
	if got != "" {
		reason = fmt.Sprintf("%s has a CNAME to %s, not %s", domain.Hostname, got, target)
	}
	if err := s.domains.SetStatus(domain.ID, apphost.DomainPending, reason, 0, s.now()); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrDomainNotPointed, reason)
}

func (s *AppDomainService) Remove(ctx context.Context, projectID, appID, id string) error {
	release, err := s.leases.holdAppForDeploy(ctx, projectID, appID)
	if err != nil {
		return err
	}
	defer release()
	app, _, err := s.lookup(projectID, appID, id)
	if err != nil {
		return err
	}
	if err := s.domains.Delete(projectID, appID, id); err != nil {
		return err
	}
	return s.SyncApp(ctx, "", app)
}

func (s *AppDomainService) lookup(projectID, appID, id string) (*apphost.App, *apphost.Domain, error) {
	app, err := s.leases.lookupApp(projectID, appID)
	if err != nil {
		return nil, nil, err
	}
	domain, err := s.domains.Get(projectID, appID, id)
	if err != nil {
		return nil, nil, err
	}
	if domain == nil {
		return nil, nil, apphost.ErrDomainNotFound
	}
	return app, domain, nil
}

// SyncApp makes the app's domain routes exactly its routable domains, under
// its current name. An empty namespace is looked up; a project without one
// has nothing to route.
func (s *AppDomainService) SyncApp(ctx context.Context, namespace string, app *apphost.App) error {
	if namespace == "" {
		inst, err := s.instances.FindByProjectID(app.ProjectID)
		if err != nil {
			return fmt.Errorf("look up project namespace: %w", err)
		}
		if inst == nil || inst.Namespace == "" {
			return nil
		}
		namespace = inst.Namespace
	}
	domains, err := s.domains.List(app.ProjectID, app.ID)
	if err != nil {
		return err
	}
	hosts := []string{}
	for _, domain := range domains {
		if apphost.DomainRoutable(domain.Status) && !app.Internal {
			hosts = append(hosts, domain.Hostname)
		}
	}
	return s.kube.SyncAppDomains(ctx, namespace, app, hosts, s.opts)
}

// Sweep follows each routed domain's certificate, and once a day checks its
// CNAME still names the app; after DomainDetachAfter failures in a row the
// route is taken away.
func (s *AppDomainService) Sweep(ctx context.Context) {
	domains, err := s.domains.ListRoutable()
	if err != nil {
		log.Printf("domain sweep: %v", err)
		return
	}
	for _, domain := range domains {
		if err := s.sweepOne(ctx, domain); err != nil {
			log.Printf("domain sweep %s: %v", domain.Hostname, err)
		}
	}
}

func (s *AppDomainService) sweepOne(ctx context.Context, domain *apphost.Domain) error {
	release, err := s.leases.holdAppForDeploy(ctx, domain.ProjectID, domain.AppID)
	if err != nil {
		return err
	}
	defer release()
	app, err := s.leases.lookupApp(domain.ProjectID, domain.AppID)
	if err != nil {
		return err
	}
	inst, err := s.instances.FindByProjectID(domain.ProjectID)
	if err != nil || inst == nil {
		return fmt.Errorf("look up project namespace: %w", errors.Join(err, errNoAppNamespace))
	}
	if domain.LastCheckedAt == nil || s.now().Sub(*domain.LastCheckedAt) >= apphost.DomainRecheckInterval {
		detached, err := s.recheck(ctx, inst.Namespace, app, domain)
		if err != nil || detached {
			return err
		}
	}
	return s.followCertificate(ctx, inst.Namespace, app, domain)
}

func (s *AppDomainService) recheck(ctx context.Context, namespace string, app *apphost.App, domain *apphost.Domain) (bool, error) {
	target, err := s.route.Hostname(app.Name, app.ProjectID)
	if err != nil {
		return false, err
	}
	got, lookupErr := s.dns.CNAME(ctx, domain.Hostname)
	if lookupErr == nil && got == target {
		domain.ConsecutiveFailures = 0
		return false, s.domains.SetStatus(domain.ID, domain.Status, domain.FailureReason, 0, s.now())
	}
	reason := fmt.Sprintf("the CNAME of %s points at %q, not %s", domain.Hostname, got, target)
	if lookupErr != nil {
		reason = fmt.Sprintf("the CNAME of %s could not be looked up: %v", domain.Hostname, lookupErr)
	}
	failures := domain.ConsecutiveFailures + 1
	if failures < apphost.DomainDetachAfter {
		domain.ConsecutiveFailures = failures
		return false, s.domains.SetStatus(domain.ID, domain.Status, reason, failures, s.now())
	}
	if err := s.domains.SetStatus(domain.ID, apphost.DomainDetached, reason, failures, s.now()); err != nil {
		return false, err
	}
	return true, s.SyncApp(ctx, namespace, app)
}

func (s *AppDomainService) followCertificate(ctx context.Context, namespace string, app *apphost.App, domain *apphost.Domain) error {
	state, err := s.kube.AppDomainCertificate(ctx, namespace, app.Name, domain.Hostname)
	if err != nil {
		return err
	}
	status, reason := apphost.DomainIssuing, ""
	switch {
	case state.Ready:
		status = apphost.DomainActive
	case state.Failure != "":
		status, reason = apphost.DomainIssueFailed, state.Failure
	}
	if status == domain.Status && reason == domain.FailureReason {
		return nil
	}
	checked := s.now()
	if domain.LastCheckedAt != nil {
		checked = *domain.LastCheckedAt
	}
	if err := s.domains.SetStatus(domain.ID, status, reason, domain.ConsecutiveFailures, checked); err != nil {
		return err
	}
	if status != apphost.DomainActive {
		return nil
	}
	// The certificate now exists, so the route can serve it.
	return s.SyncApp(ctx, namespace, app)
}

// StartSweeper runs Sweep while this replica leads. Returns a function that stops it.
func (s *AppDomainService) StartSweeper(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			if leading, err := leader.IsLeader(ctx); err == nil && leading {
				s.Sweep(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

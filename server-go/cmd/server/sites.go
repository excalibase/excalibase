package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// hostnameSweepInterval spaces the passes that route custom domains whose DNS is now in place.
const hostnameSweepInterval = 2 * time.Minute

// newSitesService builds Sites (EXC-353). Without SITES_ENABLED and a complete
// Cloudflare account it has no publisher, so the API answers "not available"
// and project deletion still refuses to forget sites it cannot remove. Limits
// follow each project's plan (EXC-620).
func newSitesService(cfg config.AppConfig, sqlStore storage.PlatformStore, limits sites.DeployLimiter,
	claimer service.ProjectOperationClaimer) *sites.Service {
	origins, ok := sqlStore.(storage.ProjectCorsEditor)
	if !ok {
		log.Fatal("sites: the platform store cannot edit a project's CORS list")
	}
	var publisher sites.Publisher
	if cfg.SitesAvailable() {
		client, err := cfworkers.New(cfworkers.Config{
			AccountID: cfg.Sites.AccountID, APIToken: cfg.Sites.APIToken, Namespace: cfg.Sites.DispatchNamespace,
			KVNamespace: cfg.Sites.HostsKVNamespaceID,
		})
		if err != nil {
			log.Fatalf("sites: %v", err)
		}
		publisher = client
		log.Printf("INFO: sites enabled under %s (dispatch namespace %s)", cfg.Sites.BaseDomain, cfg.Sites.DispatchNamespace)
	}
	store := sites.NewPostgresStore(sqlStore.DB())
	service, err := sites.NewService(store, publisher, origins, sites.Options{
		BaseDomain: cfg.Sites.BaseDomain, Hostnames: store, DeployLimits: limits, CustomDomains: customDomainOptions(cfg, sqlStore, claimer),
	})
	if err != nil {
		log.Fatalf("sites: %v", err)
	}
	return service
}

// newSiteHandler serves Sites and audits its deploy tokens (EXC-620).
func newSiteHandler(service *sites.Service, sqlStore storage.PlatformStore, instances storage.InstanceStore) *handler.SiteHandler {
	h := handler.NewSiteHandler(service)
	h.SetAuditLog(sqlStore)
	// The same checks the deploy route runs, so Studio and enforcement agree.
	h.SetTokenStanding(custommw.DeployTokenStanding{Users: sqlStore, Instances: instances, Orgs: sqlStore})
	h.SetDomainWriteLimiter(newSiteDomainWriteLimiter())
	return h
}

// siteDomainWriteBudget is how many custom domain adds and removes one project
// may make an hour, together: each is Cloudflare calls on the platform's one token.
const siteDomainWriteBudget = 6

func newSiteDomainWriteLimiter() func(http.Handler) http.Handler {
	return custommw.RateLimit(custommw.PerProject, siteDomainWriteBudget, time.Hour)
}

// customDomainOptions is the Cloudflare for SaaS zone (EXC-614); nil leaves custom domains off.
func customDomainOptions(cfg config.AppConfig, instances projectFinder, claimer service.ProjectOperationClaimer) *sites.CustomDomainOptions {
	if !cfg.SitesCustomDomainsAvailable() {
		return nil
	}
	zone, err := cfworkers.NewZoneClient(cfworkers.ZoneConfig{APIToken: cfg.Sites.APIToken, ZoneID: cfg.Sites.ZoneID})
	if err != nil {
		log.Fatalf("sites: %v", err)
	}
	log.Printf("INFO: site custom domains enabled; customers point a CNAME at %s", cfg.Sites.CustomDomainTarget())
	return &sites.CustomDomainOptions{Zone: zone, Target: cfg.Sites.CustomDomainTarget(),
		ReservedDomains: platformDomains(cfg), MaxOnZone: cfg.Sites.MaxZoneHostnames,
		Resolver: net.DefaultResolver, Projects: projectGate{instances: instances, claimer: claimer}}
}

type projectFinder interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// projectGate keeps custom domains of a project scheduled for or under
// deletion off the router, and checks them under the lifecycle lease a
// deletion holds while it unroutes.
type projectGate struct {
	instances projectFinder
	// claimer is the cross-replica lifecycle lease; nil (no platform database) holds nothing.
	claimer service.ProjectOperationClaimer
}

func (g projectGate) HoldProject(ctx context.Context, projectID string) (func(), bool, error) {
	if g.claimer == nil {
		return func() {}, true, nil
	}
	return g.claimer.Claim(ctx, projectID, service.OperationDeploy)
}

func (g projectGate) ProjectServing(_ context.Context, projectID string) (bool, error) {
	instance, err := g.instances.FindByProjectID(projectID)
	if err != nil {
		return false, err
	}
	if instance == nil {
		return true, nil
	}
	return instance.Status != string(domain.StatusPendingDeletion) && !domain.IsDeletionStatus(instance.Status), nil
}

// platformDomains are the install's own domains, which no customer may add as a custom domain.
func platformDomains(cfg config.AppConfig) []string {
	domains := []string{cfg.AppDomain, cfg.DBEndpointDomain}
	for _, raw := range []string{cfg.PublicBaseURL, cfg.StudioURL} {
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil && parsed.Hostname() != "" {
			domains = append(domains, parsed.Hostname())
		}
	}
	return domains
}

// startHostnameSweep routes custom domains once their DNS is in place and drops abandoned ones.
func startHostnameSweep(ctx context.Context, service *sites.Service) {
	if !service.CustomDomainsAvailable() {
		return
	}
	go func() {
		ticker := time.NewTicker(hostnameSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			report := service.RefreshPendingHostnames(ctx)
			if report.Activated > 0 || report.Expired > 0 || report.Failed > 0 {
				log.Printf("sites: custom domains checked=%d activated=%d expired=%d failed=%d",
					report.Checked, report.Activated, report.Expired, report.Failed)
			}
		}
	}()
}

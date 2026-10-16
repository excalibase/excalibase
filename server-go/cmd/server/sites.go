package main

import (
	"log"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// newSitesService builds Sites (EXC-353). Without SITES_ENABLED and a complete
// Cloudflare account it has no publisher, so the API answers "not available"
// and project deletion still refuses to forget sites it cannot remove. Limits
// follow each project's plan (EXC-620).
func newSitesService(cfg config.AppConfig, sqlStore storage.PlatformStore, limits sites.DeployLimiter) *sites.Service {
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
	service, err := sites.NewService(sites.NewPostgresStore(sqlStore.DB()), publisher, origins,
		sites.Options{BaseDomain: cfg.Sites.BaseDomain, DeployLimits: limits})
	if err != nil {
		log.Fatalf("sites: %v", err)
	}
	return service
}

// newSiteHandler serves Sites and audits its deploy tokens (EXC-620).
func newSiteHandler(service *sites.Service, sqlStore storage.PlatformStore) *handler.SiteHandler {
	h := handler.NewSiteHandler(service)
	h.SetAuditLog(sqlStore)
	return h
}

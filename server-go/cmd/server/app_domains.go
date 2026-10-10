package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/dnscname"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

const (
	appDomainSweepLockID   int64 = 0x6164_6f6d_3835_c0de
	appDomainSweepInterval       = time.Minute
	appDomainLookupTimeout       = 5 * time.Second
	systemResolvConf             = "/etc/resolv.conf"
)

// customDomainsOn: custom domains need app hosting and an ACME issuer; without
// an issuer they stay off. A single host's edge is its own issuer.
func customDomainsOn(cfg config.AppConfig) bool {
	return cfg.AppHostingEnabled && (cfg.AppDomainIssuer != "" || cfg.ProvisionerMode == "docker")
}

// verifyAppDomainIssuer refuses to start custom domains when their issuer cannot issue.
func verifyAppDomainIssuer(ctx context.Context, cfg config.AppConfig, kube k8s.KubeClient) error {
	if !customDomainsOn(cfg) || cfg.ProvisionerMode == "docker" {
		return nil
	}
	if kube == nil {
		return fmt.Errorf("APP_DOMAIN_ISSUER %q needs a Kubernetes client to check it", cfg.AppDomainIssuer)
	}
	return kube.ClusterIssuerReady(ctx, cfg.AppDomainIssuer)
}

func buildAppDomainService(cfg config.AppConfig, db *sql.DB, runtime service.AppRuntime, instances storage.InstanceStore,
	deploys *service.AppDeployService) *service.AppDomainService {
	if !customDomainsOn(cfg) {
		return nil
	}
	server, err := appDomainResolver(cfg)
	if err != nil {
		log.Fatalf("custom domains: %v", err)
	}
	route := appRenderOptions(cfg).Route
	domains := service.NewAppDomainService(apphost.NewPostgresAppStore(db), apphost.NewPostgresDomainStore(db), runtime, instances,
		dnscname.Resolver{Server: server, Timeout: appDomainLookupTimeout}, deploys, route.Public(),
		k8s.AppDomainOptions{Issuer: cfg.AppDomainIssuer, Route: route})
	deploys.SetDomainSync(domains.SyncApp)
	return domains
}

func appDomainResolver(cfg config.AppConfig) (string, error) {
	if cfg.AppDomainResolver != "" {
		return cfg.AppDomainResolver, nil
	}
	return dnscname.SystemServer(systemResolvConf)
}

func newAppDomainHandler(domains *service.AppDomainService) *handler.AppDomainHandler {
	if domains == nil {
		return nil
	}
	return handler.NewAppDomainHandler(domains)
}

// startAppDomainSweeper follows certificates and re-checks CNAMEs while this replica leads.
func startAppDomainSweeper(cfg config.AppConfig, sqlStore storage.PlatformStore, domains *service.AppDomainService) func() {
	if domains == nil {
		return func() {
			// custom domains are off: nothing to sweep
		}
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), appDomainSweepLockID)
	}
	return domains.StartSweeper(context.Background(), service.NewLeadership(lock), appDomainSweepInterval)
}

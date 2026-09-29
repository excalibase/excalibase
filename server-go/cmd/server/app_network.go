package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// newAppNetworkService is the per-project private network between apps
// (EXC-524); nil without the Postgres platform store or a cluster client.
func newAppNetworkService(sqlStore storage.PlatformStore, projects storage.InstanceStore,
	kube k8s.KubeClient, claimer service.ProjectOperationClaimer) *service.AppNetworkService {
	svc, ok := service.AppNetworkServiceFor(sqlStore, projects, kube, claimer)
	if !ok {
		log.Println("WARN: no Postgres platform store or cluster client — the app private network API is unavailable")
		return nil
	}
	return svc
}

// newAppNetworkHandler: without the service the route is not mounted.
func newAppNetworkHandler(svc *service.AppNetworkService) *handler.AppNetworkHandler {
	if svc == nil {
		return nil
	}
	return handler.NewAppNetworkHandler(svc)
}

// planSweeps is what a plan edit or an org's plan change re-runs.
type planSweeps interface {
	EnforceDiskCaps(ctx context.Context)
	SyncAllProjectQuotas(ctx context.Context)
}

// onPlanChange re-applies the app disk rule (EXC-523) and re-sizes every
// project's namespace quota (EXC-524) in the background, off the request.
func onPlanChange(sweeps planSweeps) func() {
	return func() {
		ctx := context.WithoutCancel(context.Background())
		go func() {
			sweeps.EnforceDiskCaps(ctx)
			sweeps.SyncAllProjectQuotas(ctx)
		}()
	}
}

// appTemplateArgs is everything a template deploy is checked against and drives (EXC-526).
type appTemplateArgs struct {
	sqlStore storage.PlatformStore
	projects storage.InstanceStore
	deployer *service.AppDeployService
	tiers    service.TierConfigSource
	disks    apphost.DiskLimits
	budget   *storagebudget.Budget
	network  *service.AppNetworkService
	vault    vaultclient.VaultClient
	claimer  service.ProjectOperationClaimer
}

// newAppTemplateHandler serves the built-in templates; a built-in that does not parse stops the server.
func newAppTemplateHandler(a appTemplateArgs) *handler.AppTemplateHandler {
	catalog, err := apptemplate.Builtins()
	if err != nil {
		log.Fatalf("app templates: %v", err)
	}
	plans := service.NewOrgPlanTiers(a.projects, a.sqlStore)
	deps := service.AppTemplateDeps{
		Apps: apphost.NewPostgresAppStore(a.sqlStore.DB()), Deployer: a.deployer, Projects: a.projects,
		Plans: plans, Limits: service.NewAppLimits(plans, a.tiers), Disks: a.disks, Budget: a.budget,
		Claimer: a.claimer,
	}
	// Left nil, not a typed nil, so the service sees "no network" and "no vault".
	if a.network != nil {
		deps.Network = a.network
	}
	if a.vault != nil {
		deps.Secrets = a.vault
	}
	return handler.NewAppTemplateHandler(service.NewAppTemplateService(catalog, deps))
}

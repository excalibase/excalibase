package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// newAppNetworkHandler wires the per-project private network between apps
// (EXC-524). Without the Postgres platform store the route is not mounted.
func newAppNetworkHandler(sqlStore storage.PlatformStore, projects storage.InstanceStore,
	kube k8s.KubeClient, claimer service.ProjectOperationClaimer) *handler.AppNetworkHandler {
	svc, ok := service.AppNetworkServiceFor(sqlStore, projects, kube, claimer)
	if !ok {
		log.Println("WARN: no Postgres platform store or cluster client — the app private network API is unavailable")
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

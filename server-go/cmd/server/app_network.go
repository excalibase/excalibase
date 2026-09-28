package main

import (
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
	settings, ok := sqlStore.(storage.ProjectAppNetworkStore)
	if !ok || kube == nil {
		log.Println("WARN: no Postgres platform store or cluster client — the app private network API is unavailable")
		return nil
	}
	return handler.NewAppNetworkHandler(service.NewAppNetworkService(settings, projects, kube, claimer))
}

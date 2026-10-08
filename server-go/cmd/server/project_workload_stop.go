package main

import (
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// wireProjectWorkloadStop makes a project deletion take the project's apps,
// routes, custom domains and function runtime down at once (EXC-567). Apps
// and per-project runtimes exist on Kubernetes only.
func wireProjectWorkloadStop(cfg config.AppConfig, k8sClient k8s.KubeClient, provSvc *service.ProvisioningService,
	appDeploySvc *service.AppDeployService) bool {
	if k8sClient == nil || cfg.ProvisionerMode == "docker" {
		return false
	}
	appDeploySvc.SetAppRoutes(cfg.AppHostingEnabled)
	provSvc.SetProjectWorkloadStopper(appDeploySvc)
	return true
}

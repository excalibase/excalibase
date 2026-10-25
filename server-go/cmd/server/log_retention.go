package main

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// logPlanSweepInterval bounds how long a new or restored project's logs keep
// the platform retention before its namespace carries its plan (EXC-659).
const logPlanSweepInterval = time.Minute

// startLogPlanSweep keeps each project's logs for its plan where Kubernetes
// runs the projects and the flag is on; it returns how many sweeps it started.
func startLogPlanSweep(ctx context.Context, cfg config.AppConfig, provSvc *service.ProvisioningService, kube k8s.KubeClient) int {
	flags := cfg.Features()
	if !features.Enabled(ctx, flags, features.LogRetention) {
		return 0
	}
	plans, ok := kube.(service.NamespacePlans)
	if !ok || cfg.ProvisionerMode == "docker" {
		log.Printf("log retention per plan: off, projects do not run on Kubernetes")
		return 0
	}
	provSvc.SetLogRetention(flags, cfg.LogRetention, plans)
	go provSvc.RunLogPlanSweep(ctx, logPlanSweepInterval)
	log.Printf("log retention per plan: %s by default, %v by plan", cfg.LogRetention.Default, cfg.LogRetention.ByPlan)
	return 1
}

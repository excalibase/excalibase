package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func internalDeployApp() *apphost.App {
	app := sampleDeployApp()
	app.Internal, app.Port, app.HealthCheckPath = true, 0, ""
	app.InternalPorts = []apphost.InternalPort{{Port: 6379, Protocol: apphost.ProtocolTCP}}
	return app
}

// An internal service has no URL and needs no app domain (EXC-525).
func TestDeployApp_InternalServiceHasNoURLAndNoRoute(t *testing.T) {
	app := internalDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.render.Route.Domain = ""

	returned, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	deploy := deploys.getByID(returned.ID)
	if deploy.Status == apphost.DeployStatusFailed {
		t.Fatalf("an internal service needs no hostname: %s", deploy.FailureReason)
	}
	if deploy.Spec.URL != "" {
		t.Errorf("an internal service has no URL, got %q", deploy.Spec.URL)
	}
	for _, workload := range kube.AppWorkloads {
		if workload.Ingress != nil || !workload.Internal {
			t.Error("an internal service must be applied without a route")
		}
	}
}

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// A deleted project's apps keep serving unless the deletion is wired to stop them (EXC-567).
func TestProjectWorkloadStop_WiredAtBoot(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "wireProjectWorkloadStop(cfg, k8sClient, provSvc, appDeploySvc)") {
		t.Error("project deletion is never wired to stop the project's apps")
	}
}

func TestProjectWorkloadStop_OnlyOnKubernetes(t *testing.T) {
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	cases := []struct {
		name string
		mode string
		kube k8s.KubeClient
		want bool
	}{
		{"kubernetes", "k8s", k8s.NewMockClient(), true},
		{"docker", "docker", k8s.NewMockClient(), false},
		{"no cluster", "k8s", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := service.NewProvisioningService(fakestore.NewInstances(), nil, nil)
			got := wireProjectWorkloadStop(config.AppConfig{ProvisionerMode: tc.mode}, tc.kube, prov, deploys)
			if got != tc.want {
				t.Fatalf("wired = %t, want %t", got, tc.want)
			}
		})
	}
}

// The chart grants provisioning access to Ingresses only when app hosting is
// on; a deletion on any other install must not ask for it (EXC-567).
func TestProjectWorkloadStop_TouchesAppRoutesOnlyWithAppHosting(t *testing.T) {
	for _, hosting := range []bool{true, false} {
		kube := k8s.NewMockClient()
		kube.WithdrawErr = errors.New("stop here")
		instances := fakestore.NewInstances()
		instances.Items["proj-1"] = &domain.DatabaseInstance{ProjectID: "proj-1", Namespace: "ns-1"}
		deploys := service.NewAppDeployService(nil, nil, kube, instances, nil, k8s.AppRenderOptions{})
		prov := service.NewProvisioningService(fakestore.NewInstances(), nil, nil)
		wireProjectWorkloadStop(config.AppConfig{ProvisionerMode: "k8s", AppHostingEnabled: hosting}, kube, prov, deploys)

		_ = deploys.StopProjectWorkloads(context.Background(), "proj-1")
		if len(kube.WithdrawOptions) != 1 || kube.WithdrawOptions[0].AppRoutes != hosting {
			t.Fatalf("app hosting %t: withdraw options %v", hosting, kube.WithdrawOptions)
		}
	}
}

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
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

package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// EXC-659: with the flag off no namespace label is written and searches keep their range.
func TestLogPlanSweepStartsOnlyWithItsFeature(t *testing.T) {
	store, _ := storage.NewFileSystemStore(t.TempDir())
	kube := k8s.NewClientFromInterfaces(fake.NewSimpleClientset(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	off := config.AppConfig{}
	on := config.AppConfig{DarkFeatures: []features.Feature{features.LogRetention}}
	for name, tc := range map[string]struct {
		cfg  config.AppConfig
		kube k8s.KubeClient
		want int
	}{
		"off":               {off, kube, 0},
		"on, no Kubernetes": {on, nil, 0},
		"on, single host":   {config.AppConfig{DarkFeatures: on.DarkFeatures, ProvisionerMode: "docker"}, kube, 0},
		"on":                {on, kube, 1},
	} {
		svc := service.NewProvisioningService(store, provisioner.NewFactory(), tc.kube)
		if got := startLogPlanSweep(ctx, tc.cfg, svc, tc.kube); got != tc.want {
			t.Errorf("%s: started %d sweeps, want %d", name, got, tc.want)
		}
	}
}

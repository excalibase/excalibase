package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// Auto-deploy (EXC-542) only happens if the watcher is started at boot.
func TestImageWatcher_StartedAtBoot(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "startImageWatcher(cfg, deps.features, sqlStore, deps.appDeploySvc)") {
		t.Error("the image watcher is never started")
	}
}

func TestImageWatcher_NeedsAppHostingAndThePlatformStore(t *testing.T) {
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	if startImageWatcher(config.AppConfig{}, nil, nil, deploys) == nil {
		t.Fatal("a stop function is always returned")
	}
	if startImageWatcher(config.AppConfig{AppHostingEnabled: true}, nil, nil, deploys) == nil {
		t.Fatal("a stop function is always returned")
	}
	if appImageWatchInterval < time.Minute {
		t.Fatalf("checking every %s is not gentle on registries", appImageWatchInterval)
	}
}

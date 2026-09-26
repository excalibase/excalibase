package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// A deploy whose rollout watch died with its replica only ends if the sweep
// that resumes it is started at boot.
func TestAppRolloutSweeper_StartedAtBoot(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "startAppRolloutSweeper(cfg, sqlStore, deps.appDeploySvc)") {
		t.Error("the app rollout sweeper is never started")
	}
}

func TestAppRolloutSweeper_RunsWhenAppHostingIsOn(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://x:x@127.0.0.1:1/x?sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()
	deploys := service.NewAppDeployService(apphost.NewPostgresAppStore(db), apphost.NewPostgresDeployStore(db),
		k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})

	stop := startAppRolloutSweeper(config.AppConfig{AppHostingEnabled: true}, nil, deploys)
	stop()
	startAppRolloutSweeper(config.AppConfig{}, nil, deploys)()
}

func TestWireAppLifecycle_AcceptsMissingPieces(t *testing.T) {
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	wireAppLifecycle(deploys, nil, nil)
	wireAppLifecycle(deploys, service.NewInProcessOperationClaimer(), vaultclient.NewHTTPClient("http://vault.invalid", "pat"))
}

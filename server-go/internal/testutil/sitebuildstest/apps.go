//go:build integration

package sitebuildstest

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/google/uuid"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// FreeAppLimits sizes app builds as the Free plan's defaults do.
var FreeAppLimits = sitebuilds.AppLimits{Limits: sitebuilds.Limits{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 900,
	ProjectConcurrency: 1}, MaxImageBytes: 2 << 30}

func appFlags(on bool) features.Flags {
	if on {
		return features.NewStatic(features.AppGitBuilds)
	}
	return features.NewStatic()
}

// AddApp makes an app in project, as the apps API would, and answers its id.
func (h *Harness) AddApp(project string) string {
	h.t.Helper()
	id := "app-" + uuid.NewString()[:8]
	doc, err := json.Marshal(map[string]string{"id": id, "projectId": project, "name": id, "image": "nginx", "status": "RUNNING"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.DB().Exec(`INSERT INTO apps (project_id, id, name, status, doc) VALUES ($1, $2, $2, 'RUNNING', $3)`,
		project, id, doc); err != nil {
		h.t.Fatalf("add an app: %v", err)
	}
	return id
}

// ConnectApp signs the harness user in and connects acme/site to the app.
func (h *Harness) ConnectApp(appID string, settings sitebuilds.AppSettings) *sitebuilds.AppConnectResult {
	h.t.Helper()
	h.SignIn()
	result, err := h.Service.ConnectApp(context.Background(), sitebuilds.AppConnectRequest{ProjectID: h.Project, AppID: appID,
		UserID: h.User, Repository: siteRepoName, Settings: settings})
	if err != nil {
		h.t.Fatalf("connect the app: %v", err)
	}
	return result
}

// SQL is the platform database the harness runs on, for staging rows a test needs.
func (h *Harness) SQL() *sql.DB { return h.db.DB() }

// FakeAppRunner stands in for the cluster's app builds: every build is ready once cloned.
type FakeAppRunner struct {
	mu   sync.Mutex
	runs []sitebuilds.AppRun
}

func (f *FakeAppRunner) StartApp(_ context.Context, run sitebuilds.AppRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

func (f *FakeAppRunner) Runs() []sitebuilds.AppRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sitebuilds.AppRun(nil), f.runs...)
}

func (f *FakeAppRunner) Status(context.Context, string) (sitebuilds.RunStatus, error) {
	return sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, nil
}

func (f *FakeAppRunner) Logs(context.Context, string) (string, error) {
	return "== docker build ==\ndone\n", nil
}
func (f *FakeAppRunner) Remove(context.Context, string) error { return nil }
func (f *FakeAppRunner) ForgetCloneCredential(context.Context, string) error {
	return nil
}

func (f *FakeAppRunner) FailureMessage(status sitebuilds.RunStatus, _ *sitebuilds.Build) string {
	return string(status.Failure)
}

// FakePublisher records each finished app build it would push and deploy.
type FakePublisher struct {
	mu        sync.Mutex
	published []sitebuilds.AppPublish
}

func (f *FakePublisher) PublishBuild(_ context.Context, publish sitebuilds.AppPublish) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, publish)
	return "deploy-of-" + publish.BuildID, nil
}

func (f *FakePublisher) Published() []sitebuilds.AppPublish {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sitebuilds.AppPublish(nil), f.published...)
}

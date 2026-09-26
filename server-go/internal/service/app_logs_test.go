package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func newLogService(t *testing.T) (*AppLogService, *k8s.MockClient, *apphost.App) {
	t.Helper()
	app := sampleDeployApp()
	kube := k8s.NewMockClient()
	instances := fakestore.NewInstances()
	instances.Items[app.ProjectID] = &domain.DatabaseInstance{ProjectID: app.ProjectID, Namespace: testDeployNamespace}
	return NewAppLogService(newFakeAppStoreForDeploy(app), instances, kube), kube, app
}

func TestAppLogs_ReadsTheAppsPodsInItsProjectsNamespace(t *testing.T) {
	svc, kube, app := newLogService(t)
	kube.AppLogLines = map[string][]k8s.AppLogLine{testDeployNamespace + "/" + app.ID: {{Pod: "p", Text: "hello"}}}
	page, err := svc.Logs(context.Background(), app.ProjectID, app.ID, k8s.AppLogOptions{TailLines: 50})
	if err != nil || len(page.Lines) != 1 || page.Lines[0].Text != "hello" {
		t.Fatalf("Logs = %v, %v", page, err)
	}
}

func TestAppLogs_AnotherProjectsAppIsNotFound(t *testing.T) {
	svc, kube, app := newLogService(t)
	if _, err := svc.Logs(context.Background(), "proj-other", app.ID, k8s.AppLogOptions{}); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(kube.Calls) != 0 {
		t.Fatalf("the cluster was asked: %v", kube.Calls)
	}
}

func TestAppLogs_NoNamespaceHasNoLines(t *testing.T) {
	svc, kube, app := newLogService(t)
	svc.instances = fakestore.NewInstances()
	page, err := svc.Logs(context.Background(), app.ProjectID, app.ID, k8s.AppLogOptions{})
	if err != nil || len(page.Lines) != 0 || len(kube.Calls) != 0 {
		t.Fatalf("Logs = %v, %v, calls %v", page, err, kube.Calls)
	}
}

func TestAppLogs_Failures(t *testing.T) {
	svc, kube, app := newLogService(t)
	kube.AppLogsErr = errors.New("api down")
	if _, err := svc.Logs(context.Background(), app.ProjectID, app.ID, k8s.AppLogOptions{}); err == nil {
		t.Fatal("want the cluster error")
	}
	svc.apps.(*fakeAppStoreForDeploy).getErr = errors.New("db down")
	if _, err := svc.Logs(context.Background(), app.ProjectID, app.ID, k8s.AppLogOptions{}); err == nil {
		t.Fatal("want the store error")
	}
}

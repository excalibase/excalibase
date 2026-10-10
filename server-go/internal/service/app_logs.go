package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// AppLogService reads an app's logs from its own pods in its own project's namespace.
type AppLogService struct {
	apps      apphost.Store
	instances storage.InstanceStore
	runtime   AppRuntime
}

func NewAppLogService(apps apphost.Store, instances storage.InstanceStore, runtime AppRuntime) *AppLogService {
	return &AppLogService{apps: apps, instances: instances, runtime: runtime}
}

// Logs answers ErrAppNotFound for an app the project does not hold, and no
// lines for a project with no namespace, where nothing has run.
func (s *AppLogService) Logs(ctx context.Context, projectID, appID string, opts k8s.AppLogOptions) (k8s.AppLogPage, error) {
	app, err := s.apps.Get(projectID, appID)
	if err != nil {
		return k8s.AppLogPage{}, fmt.Errorf("look up app: %w", err)
	}
	if app == nil {
		return k8s.AppLogPage{}, apphost.ErrAppNotFound
	}
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return k8s.AppLogPage{}, fmt.Errorf("look up project namespace: %w", err)
	}
	if inst == nil || inst.Namespace == "" {
		return k8s.AppLogPage{Lines: []k8s.AppLogLine{}}, nil
	}
	return s.runtime.AppLogs(ctx, inst.Namespace, app.ID, opts)
}

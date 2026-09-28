package service

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeAppStoreForDeploy struct {
	mu     sync.Mutex
	apps   map[string]*apphost.App
	getErr error
	// deploys, when set, is superseded by Transition the way the real store does.
	deploys       *fakeDeployStore
	transitionErr error
	deleteErr     error
	transitions   []string
}

func newFakeAppStoreForDeploy(app *apphost.App) *fakeAppStoreForDeploy {
	return &fakeAppStoreForDeploy{apps: map[string]*apphost.App{app.ProjectID + "/" + app.ID: app}}
}

func (f *fakeAppStoreForDeploy) Create(*apphost.App, int) error { return errors.New("not used") }

func (f *fakeAppStoreForDeploy) Get(projectID, id string) (*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	app, ok := f.apps[projectID+"/"+id]
	if !ok {
		return nil, nil
	}
	copied := *app
	return &copied, nil
}

func (f *fakeAppStoreForDeploy) List(projectID string) ([]*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*apphost.App{}
	for _, app := range f.apps {
		if app.ProjectID == projectID {
			copied := *app
			out = append(out, &copied)
		}
	}
	return out, nil
}
func (f *fakeAppStoreForDeploy) Update(app *apphost.App, expectedVersion int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.apps[app.ProjectID+"/"+app.ID]
	if !ok {
		return apphost.ErrAppNotFound
	}
	if stored.Version != expectedVersion {
		return apphost.ErrAppVersionConflict
	}
	copied := *app
	copied.Version = expectedVersion + 1
	// As the store's contract says: an update never moves the status.
	copied.Status = stored.Status
	f.apps[app.ProjectID+"/"+app.ID] = &copied
	app.Version = copied.Version
	return nil
}

func (f *fakeAppStoreForDeploy) Transition(projectID, id string, from []string, to string) (*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transitionErr != nil {
		return nil, f.transitionErr
	}
	app, ok := f.apps[projectID+"/"+id]
	if !ok {
		return nil, apphost.ErrAppNotFound
	}
	if !slices.Contains(from, app.Status) {
		return nil, fmt.Errorf("%w: it is %s", apphost.ErrAppStatusConflict, app.Status)
	}
	app.Status = to
	f.transitions = append(f.transitions, to)
	if f.deploys != nil {
		f.deploys.supersedeUnfinished(id)
	}
	copied := *app
	return &copied, nil
}

func (f *fakeAppStoreForDeploy) Delete(projectID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	app, ok := f.apps[projectID+"/"+id]
	if !ok {
		return apphost.ErrAppNotFound
	}
	if app.Status != apphost.StatusDeleting {
		return apphost.ErrAppStatusConflict
	}
	delete(f.apps, projectID+"/"+id)
	return nil
}

func (f *fakeAppStoreForDeploy) statusOf(projectID, id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[projectID+"/"+id]; ok {
		return app.Status
	}
	return ""
}

func (f *fakeDeployStore) supersedeUnfinished(appID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deploys {
		if d.AppID == appID && (d.Status == apphost.DeployStatusPending || d.Status == apphost.DeployStatusRolling) {
			d.Status = apphost.DeployStatusSuperseded
		}
	}
}

type fakeDeployStore struct {
	mu        sync.Mutex
	deploys   []*apphost.Deploy
	createErr error
	listErr   error
	updateErr error
	getErr    error
	// appStatus is what Finish recorded for each app, keyed by app id.
	appStatus map[string]string
	// appTier is what RecordResize recorded for each app, keyed by app id.
	appTier   map[string]domain.TierType
	recordErr error
}

func (f *fakeDeployStore) RecordResize(resize *apphost.Deploy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	revision := 0
	for _, d := range f.deploys {
		if d.AppID == resize.AppID {
			revision = max(revision, d.Revision)
		}
	}
	resize.Revision = revision + 1
	stored := *resize
	f.deploys = append(f.deploys, &stored)
	f.appTier[resize.AppID] = resize.Config.Tier
	return nil
}

func (f *fakeDeployStore) appTierOf(appID string) domain.TierType {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appTier[appID]
}

func (f *fakeDeployStore) finishSeeded(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deploys {
		if d.ID == id {
			d.Status = apphost.DeployStatusSucceeded
		}
	}
}

func newFakeDeployStore() *fakeDeployStore {
	return &fakeDeployStore{appStatus: map[string]string{}, appTier: map[string]domain.TierType{}}
}

func (f *fakeDeployStore) Create(deploy *apphost.Deploy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	revision := 0
	for _, d := range f.deploys {
		if d.AppID != deploy.AppID {
			continue
		}
		if d.Status == apphost.DeployStatusPending || d.Status == apphost.DeployStatusRolling {
			d.Status = apphost.DeployStatusSuperseded
		}
		if d.Revision > revision {
			revision = d.Revision
		}
	}
	deploy.Revision = revision + 1
	stored := *deploy
	f.deploys = append(f.deploys, &stored)
	return nil
}

func (f *fakeDeployStore) UpdateStatus(id, status, failureReason string, finishedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	for _, d := range f.deploys {
		if d.ID != id {
			continue
		}
		if d.Status != apphost.DeployStatusPending && d.Status != apphost.DeployStatusRolling {
			return nil
		}
		d.Status = status
		d.FailureReason = failureReason
		d.FinishedAt = finishedAt
		return nil
	}
	return nil
}

func (f *fakeDeployStore) Finish(id, status, failureReason string, finishedAt time.Time, appStatus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	for _, d := range f.deploys {
		if d.ID != id || (d.Status != apphost.DeployStatusPending && d.Status != apphost.DeployStatusRolling) {
			continue
		}
		d.Status, d.FailureReason, d.FinishedAt = status, failureReason, &finishedAt
		if appStatus != "" {
			f.appStatus[d.AppID] = appStatus
		}
	}
	return nil
}

func (f *fakeDeployStore) ListUnfinished() ([]*apphost.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*apphost.Deploy, 0)
	for _, d := range f.deploys {
		if d.Status == apphost.DeployStatusPending || d.Status == apphost.DeployStatusRolling {
			copied := *d
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakeDeployStore) appStatusRecorded(appID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.appStatus[appID]
	return status, ok
}

func (f *fakeDeployStore) appStatusOf(appID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appStatus[appID]
}

func (f *fakeDeployStore) ListByApp(projectID, appID string, limit int) ([]*apphost.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*apphost.Deploy, 0)
	for i := len(f.deploys) - 1; i >= 0; i-- {
		d := f.deploys[i]
		if d.ProjectID == projectID && d.AppID == appID {
			copied := *d
			out = append(out, &copied)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeDeployStore) GetLatest(projectID, appID string) (*apphost.Deploy, error) {
	deploys, err := f.ListByApp(projectID, appID, 1)
	if err != nil || len(deploys) == 0 {
		return nil, err
	}
	return deploys[0], nil
}

func (f *fakeDeployStore) Get(projectID, appID, id string) (*apphost.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, d := range f.deploys {
		if d.ID == id && d.AppID == appID && d.ProjectID == projectID {
			copied := *d
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeDeployStore) getByID(id string) *apphost.Deploy {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deploys {
		if d.ID == id {
			copied := *d
			return &copied
		}
	}
	return nil
}

func waitForDeployStatus(t *testing.T, store *fakeDeployStore, id, want string) *apphost.Deploy {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d := store.getByID(id)
		if d != nil && d.Status == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy %s did not reach status %q in time (last: %+v)", id, want, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestPauseAppForCredit_StopsARunningAppAfterTheCheckPasses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	checks := 0
	app, err := f.svc.PauseAppForCredit(context.Background(), f.app.ProjectID, f.app.ID, func() error { checks++; return nil })
	if err != nil || app.Status != apphost.StatusStopped || f.status() != apphost.StatusStopped {
		t.Fatalf("%+v %v %s", app, err, f.status())
	}
	if checks != 1 {
		t.Fatalf("the check runs once, under the lease: %d", checks)
	}
}

func TestPauseAppForCredit_TakesOnlyAnAppThatIsRunning(t *testing.T) {
	for _, status := range []string{apphost.StatusStopped, apphost.StatusFailed, apphost.StatusResuming, apphost.StatusPausing, apphost.StatusCreated} {
		f := newLifecycleFixture(t, status)
		checks := 0
		_, err := f.svc.PauseAppForCredit(context.Background(), f.app.ProjectID, f.app.ID, func() error { checks++; return nil })
		if !errors.Is(err, ErrAppNotRunning) {
			t.Errorf("%s: %v, want ErrAppNotRunning", status, err)
		}
		if f.status() != status || len(f.kube.Calls) != 0 || checks != 0 {
			t.Errorf("%s: nothing changes: status %s, calls %v, checks %d", status, f.status(), f.kube.Calls, checks)
		}
	}
}

func TestPauseAppForCredit_ACheckThatRefusesStopsBeforeAnythingChanges(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	refusal := errors.New("the org has credit again")
	_, err := f.svc.PauseAppForCredit(context.Background(), f.app.ProjectID, f.app.ID, func() error { return refusal })
	if !errors.Is(err, refusal) || f.status() != apphost.StatusRunning || len(f.kube.Calls) != 0 {
		t.Fatalf("%v %s %v", err, f.status(), f.kube.Calls)
	}
}

func TestPauseAppForCredit_AnAppThatChangedAfterTheCheckIsNotPausedAndNotCredits(t *testing.T) {
	for _, moved := range []string{apphost.StatusStopped, apphost.StatusFailed} {
		f := newLifecycleFixture(t, apphost.StatusRunning)
		_, err := f.svc.PauseAppForCredit(context.Background(), f.app.ProjectID, f.app.ID, func() error {
			f.apps.setStatus(f.app.ProjectID, f.app.ID, moved)
			return nil
		})
		if !errors.Is(err, ErrAppNotRunning) {
			t.Errorf("%s: %v, want ErrAppNotRunning, the conditional transition's refusal", moved, err)
		}
		if f.status() != moved || len(f.kube.Calls) != 0 {
			t.Errorf("%s: credit changed an app it did not stop: %s %v", moved, f.status(), f.kube.Calls)
		}
	}
}

// setStatus moves an app as another replica or the app's owner would, behind the service's back.
func (f *fakeAppStoreForDeploy) setStatus(projectID, id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[projectID+"/"+id].Status = status
}

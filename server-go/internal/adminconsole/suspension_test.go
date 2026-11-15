package adminconsole

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeSuspensions struct {
	mu        sync.Mutex
	users     map[string]*UserRef
	owned     map[string][]OwnedApp
	recorded  map[string][]SuspendedApp
	reasons   map[string]string
	suspended map[string]bool
}

func newFakeSuspensions() *fakeSuspensions {
	return &fakeSuspensions{
		users:     map[string]*UserRef{},
		owned:     map[string][]OwnedApp{},
		recorded:  map[string][]SuspendedApp{},
		reasons:   map[string]string{},
		suspended: map[string]bool{},
	}
}

func (f *fakeSuspensions) FindUser(_ context.Context, id string) (*UserRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	if u == nil {
		return nil, nil
	}
	copied := *u
	copied.Suspended = f.suspended[id]
	return &copied, nil
}

func (f *fakeSuspensions) OwnedApps(_ context.Context, userID string) ([]OwnedApp, error) {
	return f.owned[userID], nil
}

func (f *fakeSuspensions) Suspend(_ context.Context, s Suspension, apps []SuspendedApp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[s.UserID].Active = false
	f.suspended[s.UserID] = true
	f.reasons[s.UserID] = s.Reason
	f.recorded[s.UserID] = apps
	return nil
}

func (f *fakeSuspensions) Unsuspend(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[userID].Active = true
	f.suspended[userID] = false
	return nil
}

func (f *fakeSuspensions) SuspendedApps(_ context.Context, userID string) ([]SuspendedApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SuspendedApp(nil), f.recorded[userID]...), nil
}

func (f *fakeSuspensions) ForgetSuspendedApp(_ context.Context, userID, projectID, appID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.recorded[userID][:0:0]
	for _, app := range f.recorded[userID] {
		if app.ProjectID != projectID || app.AppID != appID {
			kept = append(kept, app)
		}
	}
	f.recorded[userID] = kept
	return nil
}

type fakeControl struct {
	paused, resumed []string
	failPause       map[string]error
	failResume      map[string]error
}

func (c *fakeControl) PauseApp(_ context.Context, projectID, appID string) (*apphost.App, error) {
	if err := c.failPause[appID]; err != nil {
		return nil, err
	}
	c.paused = append(c.paused, projectID+"/"+appID)
	return &apphost.App{ID: appID}, nil
}

func (c *fakeControl) ResumeApp(_ context.Context, projectID, appID, _ string) (*apphost.App, error) {
	if err := c.failResume[appID]; err != nil {
		return nil, err
	}
	c.resumed = append(c.resumed, projectID+"/"+appID)
	return &apphost.App{ID: appID}, nil
}

type captureAudit struct{ entries []*domain.AuditEntry }

func (a *captureAudit) LogAudit(_ context.Context, e *domain.AuditEntry) error {
	a.entries = append(a.entries, e)
	return nil
}

func newSuspensionFixture() (*SuspensionService, *fakeSuspensions, *fakeControl, *captureAudit) {
	store := newFakeSuspensions()
	store.users["victim"] = &UserRef{ID: "victim", Email: "v@mailinator.test", Role: "user", Active: true}
	store.users["boss"] = &UserRef{ID: "boss", Email: "boss@example.test", Role: "platform_admin", Active: true}
	store.owned["victim"] = []OwnedApp{
		{ProjectID: "p1", AppID: "a1", Name: "proxy", Status: apphost.StatusRunning, Replicas: 3},
		{ProjectID: "p1", AppID: "a2", Name: "idle", Status: apphost.StatusStopped, Replicas: 1},
		{ProjectID: "p2", AppID: "a3", Name: "web", Status: apphost.StatusRunning, Replicas: 2},
	}
	control := &fakeControl{failPause: map[string]error{}, failResume: map[string]error{}}
	audit := &captureAudit{}
	return NewSuspensionService(store, control, audit), store, control, audit
}

var admin = Actor{ID: "boss", IP: "203.0.113.9"}

func TestSuspendDeactivatesRecordsAndStopsRunningApps(t *testing.T) {
	svc, store, control, audit := newSuspensionFixture()

	result, err := svc.Suspend(context.Background(), admin, "victim", "proxy abuse")
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if store.users["victim"].Active || !store.suspended["victim"] || store.reasons["victim"] != "proxy abuse" {
		t.Fatalf("user not suspended with reason: %+v", store.users["victim"])
	}
	if got := len(control.paused); got != 2 {
		t.Fatalf("paused %v, want the two running apps", control.paused)
	}
	recorded := store.recorded["victim"]
	if len(recorded) != 2 || recorded[0].PreviousReplicas != 3 || recorded[1].PreviousReplicas != 2 {
		t.Fatalf("previous replicas not recorded: %+v", recorded)
	}
	if len(result.AppsStopped) != 2 || len(result.AppsFailed) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != AuditActionSuspend || audit.entries[0].ResourceID != "victim" || audit.entries[0].UserID != "boss" {
		t.Fatalf("audit = %+v", audit.entries)
	}
}

func TestSuspendKeepsTheAccountBlockedWhenAnAppWillNotStop(t *testing.T) {
	svc, store, control, _ := newSuspensionFixture()
	control.failPause["a3"] = errors.New("cluster busy")

	result, err := svc.Suspend(context.Background(), admin, "victim", "proxy abuse")
	if err != nil {
		t.Fatalf("a failed pause must not undo the suspension: %v", err)
	}
	if store.users["victim"].Active {
		t.Fatal("account must stay suspended")
	}
	if len(result.AppsFailed) != 1 || result.AppsFailed[0].AppID != "a3" {
		t.Fatalf("failed apps = %+v", result.AppsFailed)
	}
}

func TestSuspendRefusesWithoutAReason(t *testing.T) {
	svc, store, _, _ := newSuspensionFixture()
	if _, err := svc.Suspend(context.Background(), admin, "victim", "  "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("err = %v, want ErrReasonRequired", err)
	}
	if !store.users["victim"].Active {
		t.Fatal("nothing may change")
	}
}

func TestSuspendRefusesPlatformAccountsAndYourself(t *testing.T) {
	svc, _, _, _ := newSuspensionFixture()
	if _, err := svc.Suspend(context.Background(), Actor{ID: "other"}, "boss", "x"); !errors.Is(err, ErrProtectedAccount) {
		t.Fatalf("platform account: err = %v", err)
	}
	if _, err := svc.Suspend(context.Background(), Actor{ID: "victim"}, "victim", "x"); !errors.Is(err, ErrProtectedAccount) {
		t.Fatalf("self: err = %v", err)
	}
}

func TestSuspendUnknownUser(t *testing.T) {
	svc, _, _, _ := newSuspensionFixture()
	if _, err := svc.Suspend(context.Background(), admin, "nobody", "x"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsuspendReactivatesAndResumesOnlyWhatSuspendStopped(t *testing.T) {
	svc, store, control, audit := newSuspensionFixture()
	if _, err := svc.Suspend(context.Background(), admin, "victim", "proxy abuse"); err != nil {
		t.Fatal(err)
	}

	result, err := svc.Unsuspend(context.Background(), admin, "victim")
	if err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	if !store.users["victim"].Active || store.suspended["victim"] {
		t.Fatal("user must be active and no longer suspended")
	}
	if len(control.resumed) != 2 {
		t.Fatalf("resumed %v, want the two apps suspend stopped, not the one the user had paused", control.resumed)
	}
	if len(store.recorded["victim"]) != 0 || len(result.AppsRestored) != 2 {
		t.Fatalf("recorded = %+v result = %+v", store.recorded["victim"], result)
	}
	if last := audit.entries[len(audit.entries)-1]; last.Action != AuditActionUnsuspend {
		t.Fatalf("last audit = %+v", last)
	}
}

func TestUnsuspendKeepsTheRecordOfAnAppThatDidNotResume(t *testing.T) {
	svc, store, control, _ := newSuspensionFixture()
	if _, err := svc.Suspend(context.Background(), admin, "victim", "x"); err != nil {
		t.Fatal(err)
	}
	control.failResume["a1"] = errors.New("over plan")

	result, err := svc.Unsuspend(context.Background(), admin, "victim")
	if err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	if !store.users["victim"].Active {
		t.Fatal("account must be active even when an app cannot resume")
	}
	if len(result.AppsFailed) != 1 || len(store.recorded["victim"]) != 1 || store.recorded["victim"][0].AppID != "a1" {
		t.Fatalf("failed = %+v, kept = %+v", result.AppsFailed, store.recorded["victim"])
	}
}

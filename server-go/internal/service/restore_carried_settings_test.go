package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-639: a copy restored into a new project came back with no API
// permissions, so every signed-in query was refused. The source's settings
// are carried onto the copy before it is activated.
func TestRestoredCopyIsGivenTheSourcesSettingsBeforeItServes(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	reg.carryStatus = func(projectID string) string {
		row, _ := reg.store.FindByProjectID(projectID)
		if row == nil {
			return ""
		}
		return row.Status
	}

	if _, err := restoreDst(t, adapter, sourceInstance()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(reg.carried) != 1 || reg.carried[0] != [2]string{"src", "dst"} {
		t.Fatalf("carried %v, want the source's settings onto the copy once", reg.carried)
	}
	if reg.carriedWhile[0] != string(domain.StatusRestoring) {
		t.Errorf("settings carried while the copy was %q; it must not serve before they are in place", reg.carriedWhile[0])
	}
}

func TestRestoreFailsWhenTheSourcesSettingsCannotBeCarried(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{carryErr: errors.New("platform database down")}
	adapter := newRestoreReadyAdapter(t, mock, reg)

	_, err := restoreDst(t, adapter, sourceInstance())
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("Restore = %v, want ErrRestoreNotObserved", err)
	}
	if row, _ := reg.store.FindByProjectID("dst"); row != nil {
		t.Errorf("a copy without its settings must not be left behind: %+v", row)
	}
	if mock.Namespaces["org-dst"] {
		t.Error("a copy without its settings must compensate its namespace away")
	}
}

func TestCarryingRestoredSettingsNeedsAStore(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); !errors.Is(err, ErrRestoredSettingsNotConfigured) {
		t.Fatalf("CarryRestoredSettings = %v, want ErrRestoredSettingsNotConfigured", err)
	}
}

type recordingSettings struct{ copies [][2]string }

func (r *recordingSettings) CopyRestoredSettings(_ context.Context, sourceID, targetID string) error {
	r.copies = append(r.copies, [2]string{sourceID, targetID})
	return nil
}

func TestCarryingRestoredSettingsCopiesFromTheSource(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	settings := &recordingSettings{}
	svc.SetRestoredSettings(settings)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if len(settings.copies) != 1 || settings.copies[0] != [2]string{"src", "dst"} {
		t.Fatalf("copies = %v", settings.copies)
	}
}

// The data plane may have looked the copy up while it had no settings; once
// they are in place every cache it keeps of the copy is evicted.
func TestCarryingRestoredSettingsEvictsWhatTheDataPlaneCached(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	svc.SetRestoredSettings(&recordingSettings{})
	events := &fakePolicyPublisher{}
	svc.SetProjectEventPublisher(events)

	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
		t.Fatal(err)
	}
	want := domain.PolicyChangeEvent{ProjectID: "dst", Kind: domain.SchemaChangeKind, Op: domain.OpChangeUpdate}
	if len(events.events) != 1 || events.events[0] != want {
		t.Fatalf("events = %+v, want %+v", events.events, want)
	}
}

// EXC-639: the slot is re-checked when the copy is registered. A refusal
// there is the project limit, and the caller must be told that rather than
// that the database was not confirmed usable.
func TestRestoreRefusedAtRegistrationForWantOfASlotSaysSo(t *testing.T) {
	store := emptyInstanceStore(t)
	source := sourceInstance()
	source.OrgID = "org1"
	source.Namespace = "org1-src"
	if err := store.Create(source); err != nil {
		t.Fatal(err)
	}
	registrar := NewProvisioningService(store, provisioner.NewFactory(), nil)
	registrar.SetOrgStore(testOrgs())
	registrar.SetVault(newFakeVault())
	registrar.SetRestoredSettings(&recordingSettings{})
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, registrar)
	adapter.SetInstanceStore(store)

	_, err := restoreDst(t, adapter, source)
	var limit *OrgProjectLimitError
	if !errors.As(err, &limit) || limit.Limit != 1 || limit.Tier != domain.Free {
		t.Fatalf("Restore = %v, want the Free plan's project limit", err)
	}
	if got := publicRestoreFailure(err); got != limit.Error() {
		t.Errorf("the job reports %q, want %q", got, limit.Error())
	}
	if row, _ := store.FindByProjectID("dst"); row != nil {
		t.Errorf("a refused copy must leave no project: %+v", row)
	}
	if mock.Namespaces["org1-dst"] {
		t.Error("a refused copy must compensate its namespace away")
	}
}

var _ storage.RestoredSettingsStore = (*recordingSettings)(nil)

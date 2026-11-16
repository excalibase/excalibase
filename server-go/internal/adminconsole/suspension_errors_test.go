package adminconsole

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// flakyStore fails the one store call named in fail.
type flakyStore struct {
	*fakeSuspensions
	fail string
}

func (f flakyStore) hit(name string) error {
	if f.fail == name {
		return errBoom
	}
	return nil
}
func (f flakyStore) FindUser(ctx context.Context, id string) (*UserRef, error) {
	if err := f.hit("find"); err != nil {
		return nil, err
	}
	return f.fakeSuspensions.FindUser(ctx, id)
}
func (f flakyStore) OwnedApps(ctx context.Context, id string) ([]OwnedApp, error) {
	if err := f.hit("owned"); err != nil {
		return nil, err
	}
	return f.fakeSuspensions.OwnedApps(ctx, id)
}
func (f flakyStore) Suspend(ctx context.Context, s Suspension, apps []SuspendedApp) error {
	if err := f.hit("suspend"); err != nil {
		return err
	}
	return f.fakeSuspensions.Suspend(ctx, s, apps)
}
func (f flakyStore) Unsuspend(ctx context.Context, id string) error {
	if err := f.hit("unsuspend"); err != nil {
		return err
	}
	return f.fakeSuspensions.Unsuspend(ctx, id)
}
func (f flakyStore) SuspendedApps(ctx context.Context, id string) ([]SuspendedApp, error) {
	if err := f.hit("stopped"); err != nil {
		return nil, err
	}
	return f.fakeSuspensions.SuspendedApps(ctx, id)
}
func (f flakyStore) ForgetSuspendedApp(ctx context.Context, u, p, a string) error {
	if err := f.hit("forget"); err != nil {
		return err
	}
	return f.fakeSuspensions.ForgetSuspendedApp(ctx, u, p, a)
}

func flakyService(fail string) (*SuspensionService, *fakeSuspensions) {
	base := newFakeSuspensions()
	base.users["victim"] = &UserRef{ID: "victim", Email: "v@x.test", Role: "user", Active: true}
	base.recorded["victim"] = []SuspendedApp{{ProjectID: "p1", AppID: "a1", PreviousReplicas: 1}}
	return NewSuspensionService(flakyStore{base, fail}, &fakeControl{}, failingAudit{}), base
}

type failingAudit struct{}

func (failingAudit) LogAudit(context.Context, *domain.AuditEntry) error { return errBoom }

// Every failed step is reported; a half-done suspension is never called a success.
func TestSuspendReportsEachFailedStep(t *testing.T) {
	for _, step := range []string{"find", "owned", "suspend"} {
		service, _ := flakyService(step)
		if _, err := service.Suspend(context.Background(), admin, "victim", "proxy abuse confirmed"); !errors.Is(err, errBoom) {
			t.Errorf("%s: err = %v", step, err)
		}
	}
}

func TestUnsuspendReportsEachFailedStep(t *testing.T) {
	for _, step := range []string{"find", "unsuspend", "stopped", "forget"} {
		service, _ := flakyService(step)
		if _, err := service.Unsuspend(context.Background(), admin, "victim", "appealed and verified"); !errors.Is(err, errBoom) {
			t.Errorf("%s: err = %v", step, err)
		}
	}
}

func TestUnsuspendOfAnUnknownAccountIsNotFound(t *testing.T) {
	service, _ := flakyService("")
	if _, err := service.Unsuspend(context.Background(), admin, "ghost", "appealed and verified"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := service.Unsuspend(context.Background(), admin, "victim", "  "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("a blank reason: err = %v", err)
	}
}

func TestAReasonOverTheLimitIsRefused(t *testing.T) {
	service, _ := flakyService("")
	if _, err := service.Suspend(context.Background(), admin, "victim", strings.Repeat("x", maxReasonLength+1)); !errors.Is(err, ErrReasonTooLong) {
		t.Fatalf("err = %v", err)
	}
}

// A lost audit write is logged; the suspension it describes still stands.
func TestASuspensionStandsWhenItsAuditWriteFails(t *testing.T) {
	service, base := flakyService("")
	service.now = func() time.Time { return time.Unix(0, 0) }
	if _, err := service.Suspend(context.Background(), admin, "victim", "proxy abuse confirmed"); err != nil {
		t.Fatal(err)
	}
	if !base.suspended["victim"] {
		t.Fatal("the account must be suspended")
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// recordingSchedules is the backup scheduler as a project's lifecycle sees it.
type recordingSchedules struct {
	registered   map[string]domain.BackupSchedule
	unregistered []string
	fail         error
}

func newRecordingSchedules() *recordingSchedules {
	return &recordingSchedules{registered: map[string]domain.BackupSchedule{}}
}

func (r *recordingSchedules) Register(_ context.Context, sched *domain.BackupSchedule) error {
	if r.fail != nil {
		return r.fail
	}
	r.registered[sched.ProjectID] = *sched
	return nil
}

func (r *recordingSchedules) Unregister(_ context.Context, projectID string) error {
	delete(r.registered, projectID)
	r.unregistered = append(r.unregistered, projectID)
	return nil
}

func provisionSingleHostPostgres(t *testing.T, schedules *recordingSchedules) (*ProvisioningService, *domain.ProvisioningResponse, error) {
	t.Helper()
	svc, _, _ := setupSingleHostMySQL(t)
	if schedules != nil {
		svc.SetBackupSchedules(schedules)
	}
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "shop", OrgID: "org1", DBType: domain.PostgreSQL,
	})
	return svc, resp, err
}

// EXC-593: a single host's Postgres project runs its plan's backup schedule
// from the moment it exists, not once someone posts one.
func TestSingleHostPostgresProjectRunsItsPlansBackupSchedule(t *testing.T) {
	schedules := newRecordingSchedules()
	svc, resp, err := provisionSingleHostPostgres(t, schedules)
	if err != nil || resp.Status != "ACTIVE" {
		t.Fatalf("Provision: %v %+v", err, resp)
	}
	inst, _ := svc.store.FindByProjectID(resp.ProjectID)
	got := schedules.registered[resp.ProjectID]
	if !got.Enabled || got.Cron == "" || got.Cron != inst.BackupSchedule || got.RetentionDays != *inst.BackupRetentionDays {
		t.Fatalf("registered %+v for a project recorded with %q", got, inst.BackupSchedule)
	}
	unprotect(t, svc.store, resp.ProjectID)
	if err := svc.Deprovision(context.Background(), resp.ProjectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(schedules.unregistered) != 1 || schedules.unregistered[0] != resp.ProjectID {
		t.Fatalf("the deleted project's schedule still runs: %v", schedules.unregistered)
	}
}

// A project that must be backed up is not handed out without its schedule.
func TestSingleHostPostgresProjectFailsWithoutItsSchedule(t *testing.T) {
	schedules := newRecordingSchedules()
	schedules.fail = errors.New("store down")
	if _, resp, err := provisionSingleHostPostgres(t, schedules); err == nil && resp.Status == "ACTIVE" {
		t.Fatal("ACTIVE without a backup schedule")
	}
	if _, resp, err := provisionSingleHostPostgres(t, nil); err == nil && resp.Status == "ACTIVE" {
		t.Fatal("ACTIVE on a platform with no backup scheduler")
	}
}

package apphost

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

func TestGrowthIsWhatTheRecordHoldsAfterLessWhatItHeldBefore(t *testing.T) {
	stopped := []byte(`{"tier":"FREE","replicas":1,"status":"PAUSED"}`)
	got, err := growth(stopped, stopped, StatusRunning)
	if err != nil || got.CPUMilli != 250 || got.MemoryBytes != 256<<20 || got.Services != 0 {
		t.Fatalf("resuming a stopped app grows its CPU and memory: %+v %v", got, err)
	}
	running := []byte(`{"tier":"FREE","replicas":1,"status":"ACTIVE"}`)
	if got, err := growth(running, running, StatusRunning); err != nil || !got.IsZero() {
		t.Fatalf("a running app deployed again grows nothing: %+v %v", got, err)
	}
}

func TestGrowthRefusesARecordItCannotRead(t *testing.T) {
	good := []byte(`{"tier":"FREE","replicas":1,"status":"ACTIVE"}`)
	if _, err := growth([]byte("not json"), good, StatusRunning); err == nil {
		t.Fatal("an unreadable record before the change was read as nothing")
	}
	if _, err := growth(good, []byte("not json"), StatusRunning); err == nil {
		t.Fatal("an unreadable record after the change was read as nothing")
	}
}

func TestAnInactiveGateAdmitsAnAppWithoutReadingTheProject(t *testing.T) {
	SetDefaultPlanBudget(nil)
	delta := func(context.Context) (planbudget.Size, error) {
		t.Fatal("the delta must not run")
		return planbudget.Size{}, nil
	}
	if err := admitApp(context.Background(), defaultBudget, nil, "p", delta); err != nil {
		t.Fatal(err)
	}
}

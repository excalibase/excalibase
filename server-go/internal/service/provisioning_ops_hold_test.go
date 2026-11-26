package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The watcher restart takes the same lease a pause does and only on an ACTIVE project.
func TestHoldForWatcherRestartRefusesAProjectAPauseHolds(t *testing.T) {
	svc, store, _ := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	release, claimed, err := svc.claimer().Claim(context.Background(), inst.ProjectID, OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, _, err := svc.HoldForWatcherRestart(context.Background(), inst.ProjectID); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err = %v, want ErrProjectOperationRunning", err)
	}
	release()
	held, free, err := svc.HoldForWatcherRestart(context.Background(), inst.ProjectID)
	if err != nil || held.ProjectID != inst.ProjectID {
		t.Fatalf("a free project: %v", err)
	}
	free()
}

func TestHoldForWatcherRestartRefusesAPausedProject(t *testing.T) {
	svc, store, _ := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	inst.Status = string(domain.StatusPaused)
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.HoldForWatcherRestart(context.Background(), inst.ProjectID); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("err = %v, want ErrProjectNotActive", err)
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestHoldForMajorUpgradeTakesTheLeaseAndAsksTheRun(t *testing.T) {
	h := newRotationHarness(t)
	ctx := context.Background()
	var asked string
	inst, release, err := h.svc.HoldForMajorUpgrade(ctx, rotProject, func(status string) error { asked = status; return nil })
	if err != nil || inst.ProjectID != rotProject || asked != string(domain.StatusActive) {
		t.Fatalf("inst %v asked %q err %v", inst, asked, err)
	}
	if _, _, err := h.svc.HoldForMajorUpgrade(ctx, rotProject, func(string) error { return nil }); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("a held project is busy, got %v", err)
	}
	release()

	refused := errors.New("not now")
	if _, _, err := h.svc.HoldForMajorUpgrade(ctx, rotProject, func(string) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("the run's refusal was lost: %v", err)
	}
	if _, release, err := h.svc.HoldForMajorUpgrade(ctx, rotProject, func(string) error { return nil }); err != nil {
		t.Fatalf("a refused hold kept the lease: %v", err)
	} else {
		release()
	}

}

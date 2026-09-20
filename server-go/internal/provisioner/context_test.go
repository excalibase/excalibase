package provisioner

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testApplyLabels = "apply labels"

func TestProvisionContext_StageTracking(t *testing.T) {
	var stages []domain.ProvisioningStage
	var steps []string
	pc := NewProvisionContext(
		func(s domain.ProvisioningStage) { stages = append(stages, s) },
		func(s string) { steps = append(steps, s) },
	)

	pc.SetStage(domain.StageValidating)
	pc.SetStage(domain.StageNamespaceCreation)
	pc.SetStep("create namespace")
	pc.SetStep(testApplyLabels)

	if len(stages) != 2 || stages[1] != domain.StageNamespaceCreation {
		t.Errorf("expected 2 stages, got %v", stages)
	}
	if len(steps) != 2 || steps[1] != testApplyLabels {
		t.Errorf("expected 2 steps, got %v", steps)
	}
	if pc.Stage() != domain.StageNamespaceCreation {
		t.Errorf("Stage(): got %s", pc.Stage())
	}
	if pc.Step() != testApplyLabels {
		t.Errorf("Step(): got %s", pc.Step())
	}
}

func TestProvisionContext_StepResetOnNewStage(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	pc.SetStage(domain.StageNamespaceCreation)
	pc.SetStep("doing stuff")
	pc.SetStage(domain.StageCRDDeployment)
	if pc.Step() != "" {
		t.Errorf("expected step reset on new stage, got %q", pc.Step())
	}
}

func TestProvisionContext_RollbackLIFO(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	var order []string

	pc.RegisterCleanup("first", func(ctx context.Context) error {
		order = append(order, "first")
		return nil
	})
	pc.RegisterCleanup("second", func(ctx context.Context) error {
		order = append(order, "second")
		return nil
	})
	pc.RegisterCleanup("third", func(ctx context.Context) error {
		order = append(order, "third")
		return nil
	})

	results := pc.Rollback(context.Background())
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// LIFO order: third, second, first
	expected := []string{"third", "second", "first"}
	for i, want := range expected {
		if order[i] != want {
			t.Errorf("order[%d]: got %s, want %s", i, order[i], want)
		}
		if results[i].Name != want {
			t.Errorf("result[%d].Name: got %s, want %s", i, results[i].Name, want)
		}
		if !results[i].OK {
			t.Errorf("result[%d].OK: expected true", i)
		}
	}
}

func TestProvisionContext_RollbackContinuesOnError(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	var ran []string

	pc.RegisterCleanup("ok-1", func(ctx context.Context) error {
		ran = append(ran, "ok-1")
		return nil
	})
	pc.RegisterCleanup("fails", func(ctx context.Context) error {
		ran = append(ran, "fails")
		return errors.New("boom")
	})
	pc.RegisterCleanup("ok-2", func(ctx context.Context) error {
		ran = append(ran, "ok-2")
		return nil
	})

	results := pc.Rollback(context.Background())
	if len(ran) != 3 {
		t.Errorf("all 3 should run even on failure, got %v", ran)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Results in reverse: ok-2 (ok), fails (error), ok-1 (ok)
	if !results[0].OK || results[0].Name != "ok-2" {
		t.Errorf("results[0]: got %+v", results[0])
	}
	if results[1].OK || results[1].Name != "fails" || results[1].Error != "boom" {
		t.Errorf("results[1]: got %+v", results[1])
	}
	if !results[2].OK || results[2].Name != "ok-1" {
		t.Errorf("results[2]: got %+v", results[2])
	}
}

func TestProvisionContext_FailWrapsStageAndStep(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	pc.SetStage(domain.StageWaitingForReady)
	pc.SetStep("replica 2")

	err := pc.Fail(errors.New("pod timeout"))
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatal("expected StageError")
	}
	if se.Stage != domain.StageWaitingForReady {
		t.Errorf("Stage: got %s", se.Stage)
	}
	if se.Step != "replica 2" {
		t.Errorf("Step: got %s", se.Step)
	}
	if errors.Unwrap(err).Error() != "pod timeout" {
		t.Errorf("Unwrap: got %v", errors.Unwrap(err))
	}
	if se.Error() != "stage WAITING_FOR_READY (replica 2): pod timeout" {
		t.Errorf("Error(): got %s", se.Error())
	}
}

func TestProvisionContext_EmptyRollback(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	results := pc.Rollback(context.Background())
	if len(results) != 0 {
		t.Errorf("empty context rollback should return 0 results, got %d", len(results))
	}
}

func TestProvisionContext_CleanupCount(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	if pc.CleanupCount() != 0 {
		t.Error("initial count should be 0")
	}
	pc.RegisterCleanup("a", func(ctx context.Context) error { return nil })
	pc.RegisterCleanup("b", func(ctx context.Context) error { return nil })
	if pc.CleanupCount() != 2 {
		t.Errorf("count: got %d", pc.CleanupCount())
	}
}

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A DELETE refused because a PAUSE holds the project's lease used to say a
// deletion was already running. Nobody was deleting anything, and the caller
// was told to wait for something that did not exist. For an advisory lease
// the holder's operation is not knowable at all, so the refusal says the one
// true thing: something else is running, retry.
func TestARefusedLifecycleOperationDoesNotInventADeletion(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	if err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID: "held-proj", OrgID: "org1", Status: "ACTIVE",
		DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	claimer := NewInProcessOperationClaimer()
	svc.SetOperationClaimer(claimer)
	release, claimed, err := claimer.Claim(context.Background(), "held-proj", OperationPause)
	if err != nil || !claimed {
		t.Fatalf("hold the lease: claimed=%v err=%v", claimed, err)
	}
	defer release()

	err = svc.Deprovision(context.Background(), "held-proj")
	if !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err: got %v, want ErrProjectOperationRunning", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "deletion") {
		t.Errorf("the message must not name an operation it cannot know: %q", err)
	}
}

// Pause, resume and delete all refuse with the same neutral sentence, so a
// caller gets one contract whichever one it asked for.
func TestEveryLeaseRefusalSaysTheSameTrueThing(t *testing.T) {
	msg := ErrProjectOperationRunning.Error()
	for _, want := range []string{"another operation", "retry"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("message %q must contain %q", msg, want)
		}
	}
	for _, leak := range []string{"deletion", "pause", "resume"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Errorf("message %q must not name an operation it cannot know", msg)
		}
	}
	// It is still a busy project, so callers testing for that keep working.
	if !errors.Is(ErrProjectOperationRunning, storage.ErrProjectBusy) {
		t.Error("a lease refusal must read as a busy project")
	}
}

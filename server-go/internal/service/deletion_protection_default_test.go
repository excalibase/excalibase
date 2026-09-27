package service

import (
	"context"
	"errors"
	"testing"
)

// A registered project is protected from deletion until someone turns it off.
func TestRegisterProjectTurnsDeletionProtectionOn(t *testing.T) {
	h := newRegistrationHarness(t)

	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	saved, _ := h.store.FindByProjectID(testRegProject)
	if saved.DeletionProtection == nil || !*saved.DeletionProtection {
		t.Fatalf("deletion protection = %v, want on", saved.DeletionProtection)
	}
	if err := h.svc.Deprovision(context.Background(), testRegProject); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("Deprovision of a new project: got %v, want ErrDeletionProtected", err)
	}
}

// Turning protection off is the explicit step that makes a project deletable.
func TestDeprovisionProceedsOnceProtectionIsTurnedOff(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	if err := h.svc.SetDeletionProtection(testRegProject, false); err != nil {
		t.Fatalf("SetDeletionProtection: %v", err)
	}
	if err := h.svc.Deprovision(context.Background(), testRegProject); errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("Deprovision after turning protection off: %v", err)
	}
}

// An unconfirmed restore must stay deletable: DELETE is its only way out and
// the protection toggle cannot reach a RESTORING project.
func TestUnverifiedRestoreIsNotProtectedUntilItIsActive(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true, Unverified: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	saved, _ := h.store.FindByProjectID(testRegProject)
	if saved.DeletionProtection != nil && *saved.DeletionProtection {
		t.Fatal("a RESTORING project must not be deletion-protected")
	}

	markProjectActive(saved)
	if saved.DeletionProtection == nil || !*saved.DeletionProtection {
		t.Fatal("a restore that reaches ACTIVE must be deletion-protected")
	}
}

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func TestReapplyCredentialsSetsTheFiledPasswordsBack(t *testing.T) {
	h := newRotationHarness(t)
	inst, _ := h.store.FindByProjectID(rotProject)

	if err := h.svc.ReapplyCredentials(context.Background(), inst); err != nil {
		t.Fatalf("ReapplyCredentials: %v", err)
	}
	if len(h.kube.ExecStdin) != 1 {
		t.Fatalf("one statement batch, got %d", len(h.kube.ExecStdin))
	}
	sql := h.kube.ExecStdin[0]
	for _, want := range []string{rotOwner, roleAuthAdmin, roleApp} {
		if !strings.Contains(sql, hexOf(want)) || !strings.Contains(sql, hexOf(testutil.FixturePassword(want))) {
			t.Errorf("the %s password on file must be set back", want)
		}
	}
	if !strings.Contains(h.kube.Calls[len(h.kube.Calls)-1], rotPod) {
		t.Errorf("run on the primary: %v", h.kube.Calls)
	}
}

// A role that signs in with its certificate has no password to set.
func TestReapplyCredentialsSkipsCertificateRoles(t *testing.T) {
	h := newRotationHarness(t)
	h.vault.data[rotAppCurrent] = tenantcert.Material{Cert: "cert", Key: "key", RootCert: "ca"}.AddTo(h.vault.data[rotAppCurrent])
	inst, _ := h.store.FindByProjectID(rotProject)
	if err := h.svc.ReapplyCredentials(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.kube.ExecStdin[0], hexOf(roleApp)) {
		t.Error("a certificate role's password must not be set")
	}
}

func TestReapplyCredentialsDoesNotLeakPasswordsInErrors(t *testing.T) {
	h := newRotationHarness(t)
	h.kube.ExecError[rotPod] = errors.New("psql failed: " + testutil.FixturePassword(rotOwner))
	inst, _ := h.store.FindByProjectID(rotProject)

	err := h.svc.ReapplyCredentials(context.Background(), inst)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testutil.FixturePassword(rotOwner)) {
		t.Fatalf("password leaked: %v", err)
	}
}

func TestHoldForInPlaceRestoreAdmitsRunningAndInterruptedProjects(t *testing.T) {
	h := newRotationHarness(t)
	ctx := context.Background()

	inst, release, err := h.svc.HoldForInPlaceRestore(ctx, rotProject)
	if err != nil || inst.ProjectID != rotProject {
		t.Fatalf("an ACTIVE project is admitted: %v", err)
	}
	if _, _, err := h.svc.HoldForInPlaceRestore(ctx, rotProject); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("a held project is busy, got %v", err)
	}
	release()

	row, _ := h.store.FindByProjectID(rotProject)
	row.Status, row.CurrentStep = string(domain.StatusRestoring), domain.RestoreStepRestoreStopped
	_ = h.store.Update(row)
	_, release, err = h.svc.HoldForInPlaceRestore(ctx, rotProject)
	if err != nil {
		t.Fatalf("a project an earlier restore left stopped is admitted: %v", err)
	}
	release()

	for _, status := range []string{string(domain.StatusPaused), string(domain.StatusRestoring), string(domain.StatusDeleting)} {
		row.Status, row.CurrentStep = status, ""
		_ = h.store.Update(row)
		if _, _, err := h.svc.HoldForInPlaceRestore(ctx, rotProject); err == nil {
			t.Errorf("%s must not be admitted", status)
		}
	}
}

func TestAnnounceDatabaseReplacedEvictsTheSchemaCaches(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.AnnounceDatabaseReplaced(context.Background(), rotProject)
	if len(h.events.events) != 1 {
		t.Fatalf("events: %v", h.events.events)
	}
	evt := h.events.events[0]
	if evt.ProjectID != rotProject || evt.Kind != domain.SchemaChangeKind || evt.Op != domain.OpChangeUpdate {
		t.Errorf("event %+v", evt)
	}
	h.svc.SetProjectEventPublisher(nil)
	h.svc.AnnounceDatabaseReplaced(context.Background(), rotProject) // no publisher: nothing to do
}

func hexOf(s string) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(s)*2)
	for _, b := range []byte(s) {
		out = append(out, digits[b>>4], digits[b&0xf])
	}
	return string(out)
}

package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-522: every restored project — plain Postgres or DocumentDB — gets an
// owner credential of its own, forced onto the owner role. The source's
// password still opens the source, so it must never open the copy.

func restoreSources() map[string]*domain.DatabaseInstance {
	plain := sourceInstance()
	plain.Username = "app"
	return map[string]*domain.DatabaseInstance{"postgres": plain, "documentdb": documentDBSource()}
}

func TestARestoredProjectOwnsANewOwnerCredential(t *testing.T) {
	for name, src := range restoreSources() {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			reg := &fakeRegistrar{}
			adapter := newRestoreReadyAdapter(t, mock, reg)
			adapter.SetRestorePlanSource(backedUpEnterprisePlan())

			if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if len(reg.calls) != 1 {
				t.Fatalf("registrations: %d", len(reg.calls))
			}
			if got := reg.calls[0].Password; got != k8s.ClusterAppPassword("dst-postgres") {
				t.Errorf("owner password: got %q, want the recovered cluster's own", got)
			}
			if opts := reg.opts[0]; !opts.ResetRolePasswords || !opts.ResetAdminPassword {
				t.Errorf("every role, the owner included, must get a new password: %+v", opts)
			}
		})
	}
}

// Without a credential of its own the only one left is the source's, which
// still opens the source; the restore is refused and compensated instead.
func TestARestoreWithoutItsOwnOwnerCredentialIsRefused(t *testing.T) {
	for name, src := range restoreSources() {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			reg := &fakeRegistrar{}
			adapter := newRestoreReadyAdapter(t, mock, reg)
			adapter.SetRestorePlanSource(backedUpEnterprisePlan())
			mock.OmitClusterAppSecret = true

			_, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
			if !errors.Is(err, ErrRestoreNotObserved) {
				t.Fatalf("err: got %v, want the restore refused", err)
			}
			if !slices.Contains(mock.Calls, "ApplyCRD:org-dst/dst-postgres") {
				t.Fatalf("the refusal must come from the missing credential, not earlier: %v", mock.Calls)
			}
			if len(reg.calls) != 0 {
				t.Errorf("nothing may be registered: %v", reg.calls)
			}
			if mock.Namespaces["org-dst"] {
				t.Errorf("the restore's namespace must be compensated away: %v", mock.Namespaces)
			}
		})
	}
}

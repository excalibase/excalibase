package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const rawFailure = "dial tcp p1-postgres-rw.org1-p1.svc.cluster.local:5432: connect to 10.42.3.7 in namespace \"org1-p1\" timed out"

func assertScrubbed(t *testing.T, carrier, text string) {
	t.Helper()
	for _, leaked := range []string{"svc.cluster.local", "org1-p1", "10.42.3.7"} {
		if strings.Contains(text, leaked) {
			t.Errorf("%s still names %q: %s", carrier, leaked, text)
		}
	}
}

func TestSafeErrorScrubsTheClusterAddress(t *testing.T) {
	assertScrubbed(t, "safeError", safeError(errors.New(rawFailure)))
}

// The customer's project answer carries the failing step in words, not the cluster's.
func TestAProjectsStoredFailureTextIsScrubbedForACustomer(t *testing.T) {
	for _, carrier := range []string{"failureReason", "rollbackLog", "deletionError", "failureStep"} {
		inst := &domain.DatabaseInstance{ProjectID: "p1", Namespace: "org1-p1"}
		switch carrier {
		case "failureReason":
			inst.FailureReason = rawFailure
		case "rollbackLog":
			inst.RollbackLog = rawFailure
		case "deletionError":
			inst.DeletionError = rawFailure
		case "failureStep":
			inst.FailureStep = rawFailure
		}
		customer := visibleTo(userContext("user"), inst)
		assertScrubbed(t, carrier, customer.FailureReason+customer.RollbackLog+customer.DeletionError+customer.FailureStep)
		admin := visibleTo(userContext("platform_admin"), inst)
		if admin.FailureReason+admin.RollbackLog+admin.DeletionError+admin.FailureStep != rawFailure {
			t.Errorf("%s: a platform admin must keep the raw text", carrier)
		}
	}
}

func TestACreateAnswerFailureTextIsScrubbed(t *testing.T) {
	resp := &domain.ProvisioningResponse{Status: "FAILED", Namespace: "org1-p1", FailureReason: rawFailure, RollbackLog: rawFailure, FailureStep: rawFailure}
	got := scrubbedCreateAnswer(resp)
	assertScrubbed(t, "create answer", got.FailureReason+got.RollbackLog+got.FailureStep)
}

func TestARestoreJobFailureTextIsScrubbedForACustomer(t *testing.T) {
	router, jobs := setupBackupHandlerWithJobs(t)
	jobs.seed(domain.RestoreJob{ID: "job-1", SourceProjectID: "p1", NewProjectID: "p1-restored", Status: domain.RestoreStatusFailed, FailureReason: "dial tcp p1-postgres-rw.org1-p1.svc.cluster.local:5432: connect to 10.42.3.7 timed out"})
	w := getRestoreJob(t, router, "p1", "job-1")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	assertScrubbed(t, "restore job", w.Body.String())
}

func TestTheLifecycleErrorBodyScrubsTheFailureReason(t *testing.T) {
	h := &ProvisioningHandler{instances: &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"p1": {ProjectID: "p1", Namespace: "org1-p1", Status: "ACTIVE", FailureReason: rawFailure},
	}}}
	w := httptest.NewRecorder()
	h.writeLifecycleError(w, "p1", errors.New(rawFailure))
	assertScrubbed(t, "lifecycle error", w.Body.String())
}

func TestCredentialsWithTheFlagOffStillNameNoClusterAddress(t *testing.T) {
	r := customerRouterWithFlag(t, "user", false)
	body := answer(r, "GET", "/api/provision/p1/credentials", "")
	assertScrubbed(t, "credentials (flag off)", body)
	if !strings.Contains(body, `"password"`) {
		t.Errorf("flag off must keep the old password shape: %s", body)
	}
}

func userContext(role string) context.Context {
	return auth.SetUser(context.Background(), &domain.User{ID: "u1", Role: role, Active: true})
}

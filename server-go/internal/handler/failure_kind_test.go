package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	crashText   = `pod p1-postgres-1 in namespace "org1-p1" is in CrashLoopBackOff`
	crashRollup = `[{"name":"delete namespace org1-p1","ok":true}]`
)

func failedProjectRouter(t *testing.T, role string) chi.Router {
	t.Helper()
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{"p1": {
		ProjectID: "p1", OrgID: "org1", Namespace: "org1-p1", Status: string(domain.StageFailed),
		FailureReason: crashText, RollbackLog: crashRollup,
	}}}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	orgs.AddMember("org1", "member-1", domain.OrgRoleDeveloper)
	mock := k8s.NewMockClient()
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	h := NewProvisioningHandler(svc, memberOfOrg1{orgs})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "member-1", Role: role, Active: true})))
		})
	})
	r.Get("/api/provision/", h.ListInstances)
	r.Get("/api/provision/{projectId}/", h.GetStatus)
	return r
}

type failureFields struct {
	FailureReason string `json:"failureReason"`
	RollbackLog   string `json:"rollbackLog"`
	FailureKind   string `json:"failureKind"`
}

func decodeFailure(t *testing.T, body string) failureFields {
	t.Helper()
	var got failureFields
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("not a project answer: %v: %s", err, body)
	}
	return got
}

func firstOfList(t *testing.T, body string) failureFields {
	t.Helper()
	var list []failureFields
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 {
		t.Fatalf("expected one project: %v: %s", err, body)
	}
	return list[0]
}

func assertCustomerView(t *testing.T, what string, got failureFields, body string) {
	t.Helper()
	if got.FailureKind != "crashing" {
		t.Errorf("%s: failureKind = %q, want crashing", what, got.FailureKind)
	}
	if got.FailureReason != "" || got.RollbackLog != "" {
		t.Errorf("%s leaks raw provisioning text: %s", what, body)
	}
	for _, leaked := range []string{"CrashLoopBackOff", "org1-p1", "p1-postgres-1"} {
		if strings.Contains(body, leaked) {
			t.Errorf("%s names %q: %s", what, leaked, body)
		}
	}
}

func assertStaffView(t *testing.T, what string, got failureFields) {
	t.Helper()
	if got.FailureReason != crashText || got.RollbackLog != crashRollup {
		t.Errorf("%s lost the raw text for platform staff: %+v", what, got)
	}
	if got.FailureKind != "crashing" {
		t.Errorf("%s: failureKind = %q, want crashing", what, got.FailureKind)
	}
}

func TestACustomerReadsAFailureKindNotTheRawText(t *testing.T) {
	for _, role := range []string{"user", "platform_viewer"} {
		r := failedProjectRouter(t, role)
		status := answer(r, "GET", "/api/provision/p1/", "")
		assertCustomerView(t, role+" status", decodeFailure(t, status), status)
		list := answer(r, "GET", "/api/provision/", "")
		assertCustomerView(t, role+" list", firstOfList(t, list), list)
	}
}

func TestPlatformAdminsAndOperatorsKeepTheRawFailureText(t *testing.T) {
	for _, role := range []string{"platform_admin", "platform_operator"} {
		r := failedProjectRouter(t, role)
		assertStaffView(t, role+" status", decodeFailure(t, answer(r, "GET", "/api/provision/p1/", "")))
		assertStaffView(t, role+" list", firstOfList(t, answer(r, "GET", "/api/provision/", "")))
	}
}

func TestACreateOrAddDatabaseAnswerHasAKindForACustomer(t *testing.T) {
	resp := &domain.ProvisioningResponse{Status: "FAILED", Namespace: "org1-p1", FailureReason: crashText, RollbackLog: crashRollup}
	customer, err := json.Marshal(shownOnce(userContext("user"), resp, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	assertCustomerView(t, "create answer", decodeFailure(t, string(customer)), string(customer))
	staff, _ := json.Marshal(shownOnce(userContext("platform_operator"), resp, ""))
	assertStaffView(t, "create answer (staff)", decodeFailure(t, string(staff)))
}

func TestARestoreAnswerHasAKindForACustomer(t *testing.T) {
	resp := &domain.ProvisioningResponse{Status: "FAILED", Namespace: "org1-p1", FailureReason: crashText, RollbackLog: crashRollup}
	customer, _ := json.Marshal(customerProvisioning(userContext("user"), resp))
	assertCustomerView(t, "restore answer", decodeFailure(t, string(customer)), string(customer))
	staff, _ := json.Marshal(customerProvisioning(userContext("platform_admin"), resp))
	assertStaffView(t, "restore answer (staff)", decodeFailure(t, string(staff)))
}

func lifecycleBody(t *testing.T, role string, stored string, cause error) (string, map[string]any) {
	t.Helper()
	h := &ProvisioningHandler{instances: &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"p1": {ProjectID: "p1", Namespace: "org1-p1", Status: "ACTIVE", FailureReason: stored},
	}}}
	w := httptest.NewRecorder()
	h.writeLifecycleError(userContext(role), w, "p1", cause)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v: %s", err, w.Body.String())
	}
	return w.Body.String(), body
}

func TestTheLifecycleErrorBodyHasAKindAndNoRawTextForACustomer(t *testing.T) {
	raw, body := lifecycleBody(t, "user", crashText, errors.New(crashText))
	if _, leaked := body["failureReason"]; leaked || body["failureKind"] != "crashing" {
		t.Errorf("customer lifecycle error: %s", raw)
	}
	for _, leaked := range []string{"CrashLoopBackOff", "org1-p1", "p1-postgres-1"} {
		if strings.Contains(raw, leaked) {
			t.Errorf("customer lifecycle error names %q: %s", leaked, raw)
		}
	}
	rawStaff, staff := lifecycleBody(t, "platform_admin", crashText, errors.New(crashText))
	if staff["failureReason"] == nil || staff["failureKind"] != "crashing" {
		t.Errorf("staff lifecycle error lost the raw text: %s", rawStaff)
	}
}

func TestAnUnexpectedLifecycleErrorIsAFixedSentence(t *testing.T) {
	cause := errors.New(`patch clusters.postgresql.cnpg.io "p1-postgres" as system:serviceaccount:platform:provisioner: forbidden`)
	raw, body := lifecycleBody(t, "user", "", cause)
	if body["error"] != lifecycleUnexpected {
		t.Errorf("error = %v, want the fixed sentence: %s", body["error"], raw)
	}
	if strings.Contains(raw, "cnpg") || strings.Contains(raw, "serviceaccount") {
		t.Errorf("the body names the cluster: %s", raw)
	}
}

func TestAKnownLifecycleErrorKeepsItsOwnSentenceWithoutWhatWrapsIt(t *testing.T) {
	cause := fmt.Errorf("resume p1 at start: pod p1-postgres-1 gone: %w", service.ErrResumeNotObserved)
	raw, body := lifecycleBody(t, "user", service.ErrResumeNotObserved.Error(), cause)
	if body["error"] != service.ErrResumeNotObserved.Error() || strings.Contains(raw, "p1-postgres-1") {
		t.Errorf("lifecycle error = %s", raw)
	}
	if body["failureMessage"] != service.ErrResumeNotObserved.Error() {
		t.Errorf("the customer should read our own sentence as failureMessage: %s", raw)
	}
}

func TestAHealthyProjectsLifecycleErrorHasNoEmptyFailureFields(t *testing.T) {
	_, body := lifecycleBody(t, "user", "", errors.New("boom"))
	for _, key := range []string{"failureKind", "failureMessage", "failureReason"} {
		if _, present := body[key]; present {
			t.Errorf("%s is present on a project with no failure", key)
		}
	}
}

func TestOnlyOurOwnSentencesReachACustomerAsFailureMessage(t *testing.T) {
	safe := []string{
		service.ErrPauseBackupNotCompleted.Error(), service.ErrPauseNotObserved.Error(), service.ErrResumeNotObserved.Error(),
		service.AddInterruptedReason,
		"the restore did not complete and the database could not be put back automatically; its data from just before the restore is in backup b-1 — restore that backup into this project",
	}
	for _, text := range safe {
		customer := visibleTo(userContext("user"), &domain.DatabaseInstance{ProjectID: "p1", FailureReason: text})
		if customer.FailureMessage != text || customer.FailureReason != "" {
			t.Errorf("%q: message %q reason %q", text, customer.FailureMessage, customer.FailureReason)
		}
	}
	for _, text := range []string{crashText, "the restore failed: s3 AccessDenied bucket x", "pause cancelled: raw cluster text"} {
		customer := visibleTo(userContext("user"), &domain.DatabaseInstance{ProjectID: "p1", FailureReason: text})
		if customer.FailureMessage != "" {
			t.Errorf("%q must not reach a customer as a message, got %q", text, customer.FailureMessage)
		}
	}
}

const rawDeletionCause = "backup purge failed: s3: AccessDenied bucket excalibase-backups-prod key org1/p1/base"

func TestAStoppedDeletionsCauseIsNotShownToACustomer(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "p1", Namespace: "org1-p1", Status: "BACKUPS_PENDING_DELETE", DeletionStep: "DELETE_BACKUPS", DeletionError: rawDeletionCause}
	customer := visibleTo(userContext("user"), inst)
	if customer.DeletionError != deletionStopped || strings.Contains(customer.DeletionError, "excalibase-backups-prod") {
		t.Errorf("customer deletionError = %q", customer.DeletionError)
	}
	for _, role := range []string{"platform_admin", "platform_operator"} {
		if got := visibleTo(userContext(role), inst).DeletionError; got != rawDeletionCause {
			t.Errorf("%s deletionError = %q, want the raw cause", role, got)
		}
	}
	if got := visibleTo(userContext("user"), &domain.DatabaseInstance{ProjectID: "p1"}).DeletionError; got != "" {
		t.Errorf("no deletion error became %q", got)
	}
}

func TestUnknownFailureTextIsNeverPassedThroughForACustomer(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "p1", Namespace: "org1-p1", FailureReason: "secret-internal-detail-7"}
	customer := visibleTo(userContext("user"), inst)
	if customer.FailureReason != "" || customer.FailureKind != domain.FailureUnknown {
		t.Errorf("unknown text gave %q / %q", customer.FailureReason, customer.FailureKind)
	}
}

func TestAFailureWithNoTextHasNoKind(t *testing.T) {
	customer := visibleTo(context.Background(), &domain.DatabaseInstance{ProjectID: "p1"})
	if customer.FailureKind != "" {
		t.Errorf("a healthy project has failureKind %q", customer.FailureKind)
	}
}

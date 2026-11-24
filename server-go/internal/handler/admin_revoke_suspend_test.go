package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type fakeOwnerSuspender struct {
	userID, reason string
	closed         bool
	err            error
	calls          int
	// closures records each closure mail asked for, as "user/org".
	closures []string
	// whenClosed is how many orgs were deleted when the closure mail was asked.
	orgs             *adminOrgStore
	deletedAtClosure []bool
}

func (f *fakeOwnerSuspender) NotifyOrgClosed(_ context.Context, userID, orgID string) {
	f.closures = append(f.closures, userID+"/"+orgID)
	if f.orgs != nil {
		f.deletedAtClosure = append(f.deletedAtClosure, f.orgs.deleted)
	}
}

func (f *fakeOwnerSuspender) Suspend(_ context.Context, _ adminconsole.Actor, userID string, reason adminconsole.Reason) (*adminconsole.Result, error) {
	f.calls++
	f.userID, f.reason, f.closed = userID, reason.Text, reason.OrgClosed
	return &adminconsole.Result{}, f.err
}

func revokeRig(t *testing.T, suspender OwnerSuspender) (*AdminHandler, *adminOrgStore, *inMemoryInstanceStore, http.Handler) {
	t.Helper()
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-1": {ProjectID: "proj-1", OrgID: "org-9", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-1"},
	}}
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme", OwnerID: "owner-1"}}
	h := NewAdminHandler(newDockerProvSvc(t, store), store, orgStore, &captureAudit{}, nil, "", nil)
	h.SetOwnerSuspender(suspender)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	return h, orgStore, store, r
}

const revokeReasonBody = `{"reason":"evicting abusive tenant"}`

func revoke(r http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/org-9?cascade=true", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// EXC-757: revoking an org also suspends its owner, so the owner cannot sign
// in and start again; the reason is required and travels with the suspension.
func TestAdmin_RevokeOrg_SuspendsTheOwnerWithTheReason(t *testing.T) {
	sus := &fakeOwnerSuspender{}
	_, orgs, _, r := revokeRig(t, sus)
	w := revoke(r, `{"reason":"proxy abuse on every project"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", w.Code, w.Body.String())
	}
	if sus.calls != 1 || sus.userID != "owner-1" {
		t.Fatalf("owner not suspended: %+v", sus)
	}
	if !sus.closed {
		t.Error("a revoke deletes the org, so its suspension must carry the closure notice")
	}
	if sus.reason == "" || !strings.Contains(sus.reason, "proxy abuse on every project") {
		t.Errorf("the reason did not reach the suspension: %q", sus.reason)
	}
	if !orgs.deleted {
		t.Error("the org should still be deleted")
	}
}

func TestAdmin_RevokeOrg_RefusesWithoutAReason(t *testing.T) {
	sus := &fakeOwnerSuspender{}
	_, orgs, store, r := revokeRig(t, sus)
	for _, q := range []string{``, `{}`, `{"reason":""}`, `{"reason":"   "}`, `{"reason":"` + strings.Repeat("x", 501) + `"}`} {
		if w := revoke(r, q); w.Code != http.StatusBadRequest {
			t.Errorf("query %q: got %d, want 400", q, w.Code)
		}
	}
	if sus.calls != 0 || orgs.deleted {
		t.Error("nothing may be suspended or deleted without a reason")
	}
	if got, _ := store.FindByProjectID("proj-1"); got == nil {
		t.Error("a project was dropped without a reason")
	}
}

// If the owner cannot be suspended, nothing is torn down: an org must not be
// destroyed while its owner stays free to sign in.
func TestAdmin_RevokeOrg_DoesNothingWhenTheOwnerCannotBeSuspended(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"protected owner": {adminconsole.ErrProtectedAccount, http.StatusConflict},
		"store failure":   {errors.New("db down"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			_, orgs, store, r := revokeRig(t, &fakeOwnerSuspender{err: tc.err})
			if w := revoke(r, `{"reason":"abuse confirmed by logs"}`); w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if orgs.deleted {
				t.Error("org deleted though the owner was not suspended")
			}
			if got, _ := store.FindByProjectID("proj-1"); got == nil {
				t.Error("a project was dropped though the owner was not suspended")
			}
		})
	}
}

func TestAdmin_RevokeOrg_RefusesWhenNoSuspenderIsWired(t *testing.T) {
	_, orgs, _, r := revokeRig(t, nil)
	if w := revoke(r, `{"reason":"abuse confirmed by logs"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	if orgs.deleted {
		t.Error("org deleted without a way to suspend its owner")
	}
}

// deactivatingStore is the suspension store over the same user the login
// handler reads: suspending flips it exactly as users.active does.
type deactivatingStore struct{ users *mockUserStore }

func (d deactivatingStore) FindUser(_ context.Context, id string) (*adminconsole.UserRef, error) {
	u := d.users.users[id]
	if u == nil {
		return nil, nil
	}
	return &adminconsole.UserRef{ID: u.ID, Email: u.Email, Role: u.Role, Active: u.Active}, nil
}
func (deactivatingStore) OwnedApps(context.Context, string) ([]adminconsole.OwnedApp, error) {
	return nil, nil
}
func (d deactivatingStore) Suspend(_ context.Context, s adminconsole.Suspension, _ []adminconsole.SuspendedApp) error {
	d.users.users[s.UserID].Active = false
	return nil
}
func (deactivatingStore) ClaimMail(context.Context, string) (bool, error) { return false, nil }
func (deactivatingStore) ReleaseMail(context.Context, string) error       { return nil }
func (deactivatingStore) ClaimClosureMail(context.Context, string, string) (bool, error) {
	return false, nil
}
func (deactivatingStore) ReleaseClosureMail(context.Context, string, string) error { return nil }
func (deactivatingStore) Unsuspend(context.Context, string) error                  { return nil }
func (deactivatingStore) SuspendedApps(context.Context, string) ([]adminconsole.SuspendedApp, error) {
	return nil, nil
}
func (deactivatingStore) ForgetSuspendedApp(context.Context, string, string, string) error {
	return nil
}

type noApps struct{}

func (noApps) PauseApp(context.Context, string, string) (*apphost.App, error) { return nil, nil }
func (noApps) ResumeApp(context.Context, string, string, string) (*apphost.App, error) {
	return nil, nil
}

// An org's owner signs in fine until the org is revoked; after, the same
// correct password is refused.
func TestAdmin_RevokeOrg_ThenTheOwnersLoginIsRefused(t *testing.T) {
	users := newMockUserStore()
	owner := testutil.FixtureToken("owner")
	password := testutil.FixturePassword("owner-login")
	hash := testutil.FixtureVerifiableHash(password)
	now := time.Now()
	users.users["owner-1"] = &domain.User{ID: "owner-1", Username: owner, Email: "owner@acme.test", Role: "user", PasswordHash: hash, Active: true, EmailVerifiedAt: &now, CreatedAt: &now}
	login := setupAuthRouter(t, users, newMockTokenStore())
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, owner, password)

	if w := doRequest(login, "POST", routeLogin, body); w.Code != http.StatusOK {
		t.Fatalf("before the revoke the owner must sign in, got %d", w.Code)
	}

	svc := adminconsole.NewSuspensionService(deactivatingStore{users}, noApps{}, &captureAudit{})
	_, _, _, r := revokeRig(t, svc)
	if w := revoke(r, `{"reason":"confirmed abuse across projects"}`); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	w := doRequest(login, "POST", routeLogin, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("after the revoke the owner's login got %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Error("a session was issued to a suspended owner")
	}
}

// The reason is audited with the revoke, not only given to the suspension.
func TestAdmin_RevokeOrg_AuditsTheReason(t *testing.T) {
	audit := &syncAudit{}
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme"}}
	h := NewAdminHandler(newDockerProvSvc(t, store), store, orgStore, audit, nil, "", nil)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	if w := revoke(r, `{"reason":"proxy abuse on every project"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if entry := audit.first(t); !strings.Contains(entry.Details, `reason="proxy abuse on every project"`) {
		t.Fatalf("audit = %+v", entry)
	}
}

// A project drop needs the same written reason, audited, within the same limit.
func TestAdmin_ForceDropProject_RequiresAndAuditsAReason(t *testing.T) {
	newRig := func() (*AdminHandler, *syncAudit, *inMemoryInstanceStore, chi.Router) {
		store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
			"proj-x": {ProjectID: "proj-x", OrgID: "org-1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-x"},
		}}
		audit := &syncAudit{}
		h := NewAdminHandler(newDockerProvSvc(t, store), store, nil, audit, nil, "", nil)
		r := chi.NewRouter()
		r.Delete("/api/admin/projects/{projectId}", h.ForceDropProject)
		return h, audit, store, r
	}
	drop := func(r http.Handler, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/admin/projects/proj-x", strings.NewReader(body)))
		return w
	}
	for _, body := range []string{``, `{}`, `{"reason":"  "}`, `{"reason":"` + strings.Repeat("x", 501) + `"}`, `{"confirmDeleteBackups":true}`} {
		_, audit, store, r := newRig()
		if w := drop(r, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %.40q: got %d, want 400", body, w.Code)
		}
		if got, _ := store.FindByProjectID("proj-x"); got == nil || audit.count() != 0 {
			t.Errorf("body %.40q: a project was dropped or audited without a reason", body)
		}
	}
	_, audit, _, r := newRig()
	if w := drop(r, `{"reason":"abandoned and holding a node"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if entry := audit.first(t); !strings.Contains(entry.Details, `reason="abandoned and holding a node"`) {
		t.Fatalf("audit = %+v", entry)
	}
}

// syncAudit is an audit sink safe for the handler's fire-and-forget write.
type syncAudit struct {
	mu      sync.Mutex
	entries []*domain.AuditEntry
}

func (a *syncAudit) LogAudit(_ context.Context, e *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

// first waits for the first entry, which the handler writes in the background.
func (a *syncAudit) first(t *testing.T) *domain.AuditEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		if len(a.entries) > 0 {
			e := a.entries[0]
			a.mu.Unlock()
			return e
		}
		a.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no audit entry was written")
	return nil
}

func (a *syncAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

func (deactivatingStore) RecordSuspendedApps(context.Context, string, []adminconsole.SuspendedApp) error {
	return nil
}

// The closure mail goes out once the projects are really gone, and before the
// org row is removed.
func TestAdmin_RevokeOrg_MailsTheClosureAfterTheTeardown(t *testing.T) {
	sus := &fakeOwnerSuspender{}
	_, orgs, _, r := revokeRig(t, sus)
	sus.orgs = orgs
	if w := revoke(r, revokeReasonBody); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(sus.closures) != 1 || sus.closures[0] != "owner-1/org-9" {
		t.Fatalf("closure mails = %v, want one for owner-1/org-9", sus.closures)
	}
}

// A revoke that left projects behind keeps the org and tells the owner nothing
// yet: the mail stays owed for the revoke that finishes the job.
func TestAdmin_RevokeOrg_NoClosureMailWhenAProjectDropFails(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-stuck": {ProjectID: "proj-stuck", OrgID: "org-9", DBType: domain.PostgreSQL, Namespace: "org-9-stuck", Status: "ACTIVE"},
	}}
	mock := k8s.NewMockClient()
	mock.Namespaces["org-9-stuck"] = true
	mock.StuckNamespaces["org-9-stuck"] = true
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	pgProv.SetDeletionPoller(provisioner.NewPoller(0, 0))
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(pgProv), mock)
	orgs := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme", OwnerID: "owner-1"}}
	sus := &fakeOwnerSuspender{}
	h := NewAdminHandler(provSvc, store, orgs, &captureAudit{}, nil, "", nil)
	h.SetOwnerSuspender(sus)
	router := chi.NewRouter()
	router.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)

	w := revoke(router, revokeReasonBody)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("got %d %s, want 207", w.Code, w.Body.String())
	}
	if len(sus.closures) != 0 || orgs.deleted {
		t.Fatalf("closure mails = %v, org deleted = %v", sus.closures, orgs.deleted)
	}
}

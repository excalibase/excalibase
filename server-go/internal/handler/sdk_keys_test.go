package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sdkkeys"
)

type fakeSDKKeys struct {
	created  []sdkkeys.CreateRequest
	revoked  []int64
	listErr  error
	createFn func() (*sdkkeys.CreatedKey, error)
	calls    []string
}

func (f *fakeSDKKeys) List(_ context.Context, orgSlug, projectID string) ([]sdkkeys.Key, error) {
	f.calls = append(f.calls, "list "+orgSlug+"/"+projectID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []sdkkeys.Key{{ID: 7, KeyPrefix: "abc", KeyType: "publishable", Name: "web"}}, nil
}

func (f *fakeSDKKeys) Create(_ context.Context, orgSlug, projectID string, req sdkkeys.CreateRequest) (*sdkkeys.CreatedKey, error) {
	f.calls = append(f.calls, "create "+orgSlug+"/"+projectID)
	f.created = append(f.created, req)
	if f.createFn != nil {
		return f.createFn()
	}
	return &sdkkeys.CreatedKey{Key: sdkkeys.Key{ID: 8, KeyPrefix: "xyz", KeyType: req.KeyType, Name: req.Name}, Plaintext: "esk_sec_live_xyz"}, nil
}

func (f *fakeSDKKeys) Revoke(_ context.Context, orgSlug, projectID string, keyID int64) error {
	f.calls = append(f.calls, "revoke "+orgSlug+"/"+projectID)
	f.revoked = append(f.revoked, keyID)
	return nil
}

type sdkKeyAudit struct{ entries []*domain.AuditEntry }

func (a *sdkKeyAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

type sdkKeyOrgs struct{ orgs map[string]*domain.Org }

func (s sdkKeyOrgs) FindOrgByID(_ context.Context, id string) (*domain.Org, error) {
	return s.orgs[id], nil
}

func newSDKKeysRouter(keys SDKKeyManager) (chi.Router, *sdkKeyAudit) {
	instances := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-a":      {ProjectID: "proj-a", OrgID: "org-1"},
		"proj-orphan": {ProjectID: "proj-orphan", OrgID: "org-gone"},
	}}
	audit := &sdkKeyAudit{}
	h := NewSDKKeysHandler(keys, instances, sdkKeyOrgs{orgs: map[string]*domain.Org{"org-1": {ID: "org-1", Slug: "acme"}}}, audit)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: "dev-1"})))
		})
	})
	r.Route("/api/projects/{projectId}/sdk-keys", h.Routes)
	return r, audit
}

func TestSDKKeysCreateShowsTheKeyOnceAndAuditsOnlyItsPrefix(t *testing.T) {
	keys := &fakeSDKKeys{}
	r, audit := newSDKKeysRouter(keys)

	w := doRequest(r, "POST", "/api/projects/proj-a/sdk-keys/", `{"name":"server","keyType":"secret"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("a response carrying a key must not be cached")
	}
	var created sdkkeys.CreatedKey
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Plaintext != "esk_sec_live_xyz" || keys.calls[0] != "create acme/proj-a" {
		t.Fatalf("created %+v via %v", created, keys.calls)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "sdk_key.create" || audit.entries[0].UserID != "dev-1" ||
		strings.Contains(audit.entries[0].Details, "esk_") || !strings.Contains(audit.entries[0].Details, "xyz") {
		t.Fatalf("audit: %+v", audit.entries)
	}
}

func TestSDKKeysCreateValidatesTheRequest(t *testing.T) {
	keys := &fakeSDKKeys{}
	r, _ := newSDKKeysRouter(keys)
	for _, body := range []string{`not json`, `{"name":"x","keyType":"admin"}`, `{"name":"` + strings.Repeat("n", 101) + `","keyType":"secret"}`} {
		if w := doRequest(r, "POST", "/api/projects/proj-a/sdk-keys/", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, w.Code)
		}
	}
	if len(keys.created) != 0 {
		t.Fatal("an invalid request reached auth")
	}
}

func TestSDKKeysListAndRevoke(t *testing.T) {
	keys := &fakeSDKKeys{}
	r, audit := newSDKKeysRouter(keys)

	w := doRequest(r, "GET", "/api/projects/proj-a/sdk-keys/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"keyPrefix":"abc"`) || strings.Contains(w.Body.String(), "plaintext") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := doRequest(r, "DELETE", "/api/projects/proj-a/sdk-keys/7", ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if len(keys.revoked) != 1 || keys.revoked[0] != 7 || audit.entries[0].Action != "sdk_key.revoke" {
		t.Fatalf("revoked %v audit %+v", keys.revoked, audit.entries)
	}
	if w := doRequest(r, "DELETE", "/api/projects/proj-a/sdk-keys/abc", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestSDKKeysUnknownProjectOrOrgIsRefused(t *testing.T) {
	keys := &fakeSDKKeys{}
	r, _ := newSDKKeysRouter(keys)
	if w := doRequest(r, "GET", "/api/projects/proj-missing/sdk-keys/", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", w.Code)
	}
	if w := doRequest(r, "GET", "/api/projects/proj-orphan/sdk-keys/", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("project without an org: %d", w.Code)
	}
	if len(keys.calls) != 0 {
		t.Fatal("auth was called for a project that could not be resolved")
	}
}

func TestSDKKeysMapsAuthFailures(t *testing.T) {
	cases := map[error]int{
		sdkkeys.ErrAuthUnavailable: http.StatusServiceUnavailable,
		&sdkkeys.RefusedError{Status: http.StatusBadRequest, Message: "bad"}: http.StatusBadRequest,
		&sdkkeys.RefusedError{Status: http.StatusForbidden, Message: "no"}:   http.StatusBadGateway,
		errors.New("boom"): http.StatusInternalServerError,
	}
	for cause, want := range cases {
		r, _ := newSDKKeysRouter(&fakeSDKKeys{listErr: cause})
		if w := doRequest(r, "GET", "/api/projects/proj-a/sdk-keys/", ""); w.Code != want {
			t.Errorf("%v: got %d, want %d", cause, w.Code, want)
		}
	}
}

func TestSDKKeysWithoutAuthConfiguredIs503(t *testing.T) {
	r, _ := newSDKKeysRouter(nil)
	if w := doRequest(r, "GET", "/api/projects/proj-a/sdk-keys/", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

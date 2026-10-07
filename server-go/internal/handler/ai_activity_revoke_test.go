package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/go-chi/chi/v5"
)

const activityOrg = "org-1"

// fakeRevokeStore holds the tokens, the project's MCP calls and the org's members.
type fakeRevokeStore struct {
	tokens  map[string]*domain.AccessToken
	used    map[string]bool // "project/hash"
	members map[string]string
	audited []domain.AuditEntry
}

func newFakeRevokeStore() *fakeRevokeStore {
	return &fakeRevokeStore{
		tokens: map[string]*domain.AccessToken{
			"mine":      {TokenHash: "mine", TokenPrefix: "excali_mi", UserID: "me", Name: "laptop"},
			"theirs":    {TokenHash: "theirs", TokenPrefix: "excali_th", UserID: "teammate", Name: "ci"},
			"outsider":  {TokenHash: "outsider", TokenPrefix: "excali_ou", UserID: "stranger", Name: "x"},
			"elsewhere": {TokenHash: "elsewhere", TokenPrefix: "excali_el", UserID: "teammate", Name: "y"},
		},
		used:    map[string]bool{"proj-a/mine": true, "proj-a/theirs": true, "proj-a/outsider": true, "proj-b/elsewhere": true},
		members: map[string]string{"me": "", "teammate": domain.OrgRoleDeveloper},
	}
}

func (f *fakeRevokeStore) ProjectAuditUsedToken(_ context.Context, projectID, via, tokenHash string) (bool, error) {
	return via == domain.AuditViaMCP && f.used[projectID+"/"+tokenHash], nil
}

func (f *fakeRevokeStore) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	return f.tokens[hash], nil
}

func (f *fakeRevokeStore) DeleteToken(_ context.Context, hash string) error {
	delete(f.tokens, hash)
	return nil
}

func (f *fakeRevokeStore) GetOrgMember(_ context.Context, orgID, userID string) (*domain.OrgMember, error) {
	role, ok := f.members[userID]
	if orgID != activityOrg || !ok {
		return nil, nil
	}
	return &domain.OrgMember{OrgID: orgID, UserID: userID, Role: role}, nil
}

func (f *fakeRevokeStore) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	f.audited = append(f.audited, *entry)
	return nil
}

// asMember serves a request as "me" holding role in the project's org, the
// way RequireProjectAccess leaves it on the context.
func asMember(t *testing.T, h *AIActivityHandler, method, target, role string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/ai-activity", h.Routes)
	req := httptest.NewRequest(method, target, nil)
	ctx := auth.SetUser(req.Context(), &domain.User{ID: "me", Active: true})
	ctx = auth.SetToken(ctx, &domain.AccessToken{TokenHash: "session", Scopes: auth.ScopeSession})
	ctx = middleware.WithProjectAccess(ctx, &middleware.ProjectAccess{
		Instance: &domain.DatabaseInstance{ProjectID: "proj-a", OrgID: activityOrg},
		Member:   &domain.OrgMember{OrgID: activityOrg, UserID: "me", Role: role},
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func revokeHandler(store *fakeRevokeStore) *AIActivityHandler {
	h := NewAIActivityHandler(&fakeProjectAudit{}, fakeTokenList{})
	h.SetRevokeStore(store)
	return h
}

// TestAIActivityRevokeAuthorization is the matrix: anyone revokes their own
// token; an org owner or admin revokes any org member's token the project's
// feed shows; a developer revokes nobody else's.
func TestAIActivityRevokeAuthorization(t *testing.T) {
	cases := []struct {
		name, role, token string
		want              int
	}{
		{"owner revokes a member's token", domain.OrgRoleOwner, "theirs", http.StatusOK},
		{"admin revokes a member's token", domain.OrgRoleAdmin, "theirs", http.StatusOK},
		{"developer revokes their own", domain.OrgRoleDeveloper, "mine", http.StatusOK},
		{"admin revokes their own", domain.OrgRoleAdmin, "mine", http.StatusOK},
		{"developer may not revoke a teammate's", domain.OrgRoleDeveloper, "theirs", http.StatusForbidden},
		{"viewer may not revoke a teammate's", domain.OrgRoleViewer, "theirs", http.StatusForbidden},
		{"admin may not revoke a non-member's", domain.OrgRoleAdmin, "outsider", http.StatusForbidden},
		{"a token this project never saw", domain.OrgRoleOwner, "elsewhere", http.StatusNotFound},
		{"an unknown token", domain.OrgRoleOwner, "nope", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeRevokeStore()
			before := store.tokens[tc.token]
			w := asMember(t, revokeHandler(store), http.MethodDelete, "/api/projects/proj-a/ai-activity/tokens/"+tc.token, tc.role)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			_, kept := store.tokens[tc.token]
			if tc.want == http.StatusOK {
				if kept {
					t.Fatal("the token is still there")
				}
				if len(store.audited) != 1 || store.audited[0].Action != auditActionTokenRevoke ||
					store.audited[0].UserID != before.UserID || !strings.Contains(store.audited[0].Details, `"revokedBy":"me"`) ||
					store.audited[0].ProjectID != "proj-a" {
					t.Fatalf("audit = %+v", store.audited)
				}
				return
			}
			if before != nil && !kept {
				t.Fatal("a refused revoke deleted the token")
			}
			if len(store.audited) != 0 {
				t.Fatalf("a refused revoke was audited as done: %+v", store.audited)
			}
		})
	}
}

func TestAIActivityRevokeByANarrowerTokenIsRefused(t *testing.T) {
	store := newFakeRevokeStore()
	h := revokeHandler(store)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/ai-activity", h.Routes)
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/proj-a/ai-activity/tokens/theirs", nil)
	ctx := auth.SetUser(req.Context(), &domain.User{ID: "me", Active: true})
	ctx = auth.SetToken(ctx, &domain.AccessToken{TokenHash: "pat", Scopes: auth.ScopeWrite, ProjectID: "proj-b"})
	ctx = middleware.WithProjectAccess(ctx, &middleware.ProjectAccess{
		Instance: &domain.DatabaseInstance{ProjectID: "proj-a", OrgID: activityOrg},
		Member:   &domain.OrgMember{OrgID: activityOrg, UserID: "me", Role: domain.OrgRoleOwner},
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusForbidden || store.tokens["theirs"] == nil {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAIActivityRevokeWithoutAStoreIsUnavailable(t *testing.T) {
	h := NewAIActivityHandler(&fakeProjectAudit{}, fakeTokenList{})
	if w := asMember(t, h, http.MethodDelete, "/api/projects/proj-a/ai-activity/tokens/mine", domain.OrgRoleOwner); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}

// TestAIActivityOffersAdminsRevokeOnEveryLiveToken pins what the feed shows
// an owner or admin: the revoke id of any member's live token.
func TestAIActivityOffersAdminsRevokeOnEveryLiveToken(t *testing.T) {
	at := time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC)
	audit := &fakeProjectAudit{entries: []domain.AuditEntry{
		{ID: 2, UserID: "teammate", ResourceID: "list_tables", Details: `{"tokenName":"ci"}`, TokenHash: "theirs", Timestamp: &at},
		{ID: 1, UserID: "teammate", ResourceID: "list_tables", Details: `{"tokenName":"old"}`, TokenHash: "gone", Timestamp: &at},
	}}
	tokens := fakeTokenList{"teammate": {{TokenHash: "theirs"}}}
	for role, wantID := range map[string]string{domain.OrgRoleAdmin: "theirs", domain.OrgRoleOwner: "theirs", domain.OrgRoleDeveloper: ""} {
		h := NewAIActivityHandler(audit, tokens)
		h.SetRevokeStore(newFakeRevokeStore())
		w := asMember(t, h, http.MethodGet, "/api/projects/proj-a/ai-activity/", role)
		var body struct {
			Calls []aiActivityView `json:"calls"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Calls) != 2 {
			t.Fatalf("%s: %v %s", role, err, w.Body.String())
		}
		if body.Calls[0].TokenID != wantID {
			t.Errorf("%s sees revoke id %q, want %q", role, body.Calls[0].TokenID, wantID)
		}
		if wantID != "" && (!body.Calls[1].TokenRevoked || body.Calls[1].TokenID != "") {
			t.Errorf("%s: a revoked token offers no revoke: %+v", role, body.Calls[1])
		}
	}
}

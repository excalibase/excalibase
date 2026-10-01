package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-536: Studio's access-token page lists, creates and revokes the
// caller's personal access tokens through these routes.

const (
	tokenServiceUser = "svc-1"
	actionCreate     = "token.create"
	actionRevoke     = "token.revoke"
)

func listTokenViews(t *testing.T, f *tokenFixture, caller string) []map[string]interface{} {
	t.Helper()
	w := doAuthRequest(f.r, "GET", routeAuthTokens, caller, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	var views []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return views
}

func viewNames(views []map[string]interface{}) map[string]bool {
	names := map[string]bool{}
	for _, v := range views {
		names[v["name"].(string)] = true
	}
	return names
}

func auditOf(f *tokenFixture, action string) []*domain.AuditEntry {
	var out []*domain.AuditEntry
	for _, e := range f.audit.entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestListTokens_ShowsOnlyTheCallersPersonalAccessTokens(t *testing.T) {
	f := newTokenFixture(t)
	f.us.users[tokenServiceUser] = &domain.User{ID: tokenServiceUser, Kind: domain.UserKindService, Role: "platform_admin", Active: true}
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	f.mint("ci-read", tokenUser, scopesRead, nil)
	f.mint("legacy", tokenUser, "", nil)
	f.mint("bob-ci", tokenOtherUser, scopesRead, nil)
	svc := f.mint("svc-auth", tokenServiceUser, "", nil)
	f.ts.tokens[auth.HashToken(svc)].Permissions = []string{"vault:read"}

	names := viewNames(listTokenViews(t, f, session))

	if !names["ci-read"] || !names["legacy"] {
		t.Errorf("the caller's access tokens must be listed, got %v", names)
	}
	for _, hidden := range []string{"studio-session", "bob-ci", "svc-auth"} {
		if names[hidden] {
			t.Errorf("%q must not be listed: sessions, other users' and service tokens never appear (got %v)", hidden, names)
		}
	}
}

func TestListTokens_ViewCarriesWhatThePageShowsAndNoSecret(t *testing.T) {
	f := newTokenFixture(t)
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	raw := f.mint("ci-read", tokenUser, scopesRead, nil)
	used := time.Now().Add(-time.Hour).UTC()
	f.ts.tokens[auth.HashToken(raw)].LastUsed = &used
	f.ts.tokens[auth.HashToken(raw)].ProjectID = "proj-1"

	views := listTokenViews(t, f, session)
	if len(views) != 1 {
		t.Fatalf("expected 1 token, got %v", views)
	}
	view := views[0]
	if view["id"] != auth.HashToken(raw) {
		t.Errorf("id must be the route identifier revoke takes, got %v", view["id"])
	}
	for field, want := range map[string]interface{}{"name": "ci-read", "scopes": scopesRead, "projectId": "proj-1", "tokenPrefix": auth.TokenPrefix(raw)} {
		if view[field] != want {
			t.Errorf("%s: got %v want %v", field, view[field], want)
		}
	}
	for _, field := range []string{"createdAt", "lastUsed"} {
		if view[field] == nil {
			t.Errorf("%s missing from %v", field, view)
		}
	}
	body, _ := json.Marshal(view)
	if strings.Contains(string(body), raw) {
		t.Error("the list must never carry the raw token")
	}
}

func TestListTokens_IdRevokesThatToken(t *testing.T) {
	f := newTokenFixture(t)
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	raw := f.mint("ci-read", tokenUser, scopesRead, nil)

	id := listTokenViews(t, f, session)[0]["id"].(string)
	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+id, session, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	if f.meStatus(raw) != http.StatusUnauthorized {
		t.Error("a revoked token must stop authenticating")
	}
	if len(listTokenViews(t, f, session)) != 0 {
		t.Error("a revoked token must leave the list")
	}
}

func TestCreateToken_IsAuditedWithoutTheSecret(t *testing.T) {
	f := newTokenFixture(t)
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, session, `{"name":"storefront","scopes":["read"],"expiresIn":"30d"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	raw := decodeBody(t, w.Body.String())["token"].(string)

	entries := auditOf(f, actionCreate)
	if len(entries) != 1 {
		t.Fatalf("expected one %s entry, got %d", actionCreate, len(entries))
	}
	entry := entries[0]
	if entry.UserID != tokenUser || entry.ResourceID != auth.TokenPrefix(raw) || entry.Resource != auditResourceToken {
		t.Errorf("entry: %+v", entry)
	}
	if !strings.Contains(entry.Details, `"storefront"`) || !strings.Contains(entry.Details, `"read"`) {
		t.Errorf("details must name the token and its scopes: %s", entry.Details)
	}
	if strings.Contains(entry.Details, raw) || strings.Contains(entry.Details, auth.HashToken(raw)) {
		t.Error("the audit entry must never carry the secret or its hash")
	}
}

func TestCreateToken_RefusedMintIsNotAudited(t *testing.T) {
	f := newTokenFixture(t)
	reader := f.mint("ci-read", tokenUser, scopesRead, nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, reader, `{"name":"wider","scopes":["write"]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a read-only token minting write: "+statusFmt, w.Code, w.Body.String())
	}
	if len(auditOf(f, actionCreate)) != 0 {
		t.Error("a refused mint must not be recorded as a creation")
	}
}

func TestRevokeToken_IsAudited(t *testing.T) {
	f := newTokenFixture(t)
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	raw := f.mint("ci-read", tokenUser, scopesRead, nil)

	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(raw), session, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	entries := auditOf(f, actionRevoke)
	if len(entries) != 1 {
		t.Fatalf("expected one %s entry, got %d", actionRevoke, len(entries))
	}
	if entries[0].UserID != tokenUser || entries[0].ResourceID != auth.TokenPrefix(raw) {
		t.Errorf("entry: %+v", entries[0])
	}
	if strings.Contains(entries[0].Details, auth.HashToken(raw)) {
		t.Error("the audit entry must never carry the token hash")
	}
}

func TestRevokeToken_PlatformAdminRevokingAnotherUsersTokenNamesTheActor(t *testing.T) {
	f := newTokenFixture(t)
	f.us.users[tokenOtherUser].Role = "platform_admin"
	admin := f.mint("admin-session", tokenOtherUser, auth.ScopeSession, nil)
	victim := f.mint("leaked", tokenUser, scopesRead, nil)

	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(victim), admin, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	entries := auditOf(f, actionRevoke)
	if len(entries) != 1 || entries[0].UserID != tokenUser || !strings.Contains(entries[0].Details, tokenOtherUser) {
		t.Fatalf("the entry must belong to the owner and name the admin who revoked it: %+v", entries)
	}
}

func TestRevokeToken_HumanCannotRevokeAServiceToken(t *testing.T) {
	f := newTokenFixture(t)
	f.us.users[tokenServiceUser] = &domain.User{ID: tokenServiceUser, Kind: domain.UserKindService, Role: "platform_admin", Active: true}
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	svc := f.mint("svc-auth", tokenServiceUser, "", nil)
	f.ts.tokens[auth.HashToken(svc)].Permissions = []string{"vault:read"}

	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(svc), session, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	if f.ts.tokens[auth.HashToken(svc)] == nil {
		t.Error("the service token must survive a refused revoke")
	}
	if len(auditOf(f, actionRevoke)) != 0 {
		t.Error("a refused revoke must not be audited as a revocation")
	}
}

func TestRevokeToken_AnotherUserCannotRevokeOrLearnAboutMyToken(t *testing.T) {
	f := newTokenFixture(t)
	bob := f.mint("bob-session", tokenOtherUser, auth.ScopeSession, nil)
	mine := f.mint("ci-read", tokenUser, scopesRead, nil)

	if names := viewNames(listTokenViews(t, f, bob)); names["ci-read"] {
		t.Error("another user's list must not show my token")
	}
	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(mine), bob, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	if f.meStatus(mine) != http.StatusOK {
		t.Error("my token must survive another user's revoke attempt")
	}
}

type failingAudit struct{}

func (failingAudit) LogAudit(context.Context, *domain.AuditEntry) error {
	return errors.New("audit store down")
}

func TestCreateAndRevoke_AnAuditFailureDoesNotUndoTheChange(t *testing.T) {
	us, ts := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: tokenUser, Role: "user", Active: true, CreatedAt: &now}
	us.users[tokenUser] = user
	h := NewAuthHandler(us, ts)
	h.SetAuditLog(failingAudit{})
	r, caller := setupAuthRouterWithUser(t, us, ts, user)
	r.With(auth.RequireAuth).Post("/audited", h.CreateToken)
	r.With(auth.RequireAuth).Delete("/audited/{tokenHash}", h.RevokeToken)

	w := doAuthRequest(r, "POST", "/audited", caller, `{"name":"ci","scopes":["read"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	raw := decodeBody(t, w.Body.String())["token"].(string)
	w = doAuthRequest(r, "DELETE", "/audited/"+auth.HashToken(raw), caller, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
}

func TestRevokeToken_ANarrowedTokenCannotRevokeABroaderSibling(t *testing.T) {
	f := newTokenFixture(t)
	bound := f.mint("ci-bound", tokenUser, scopesReadWrite, nil)
	f.ts.tokens[auth.HashToken(bound)].ProjectID = "proj-1"
	unbound := f.mint("deploy", tokenUser, scopesReadWrite, nil)

	w := doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(unbound), bound, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	if f.meStatus(unbound) != http.StatusOK {
		t.Error("the broader token must survive")
	}
	w = doAuthRequest(f.r, "DELETE", routeAuthTokens+"/"+auth.HashToken(bound), bound, "")
	if w.Code != http.StatusOK {
		t.Fatalf("a token may still revoke itself: "+statusFmt, w.Code, w.Body.String())
	}
}

func TestCreateToken_AuditNamesTheCaller(t *testing.T) {
	f := newTokenFixture(t)
	session := f.mint("studio-session", tokenUser, auth.ScopeSession, nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, session, `{"name":"ci","scopes":["read"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	entries := auditOf(f, actionCreate)
	if len(entries) != 1 || !strings.Contains(entries[0].Details, `"createdBy":"`+tokenUser+`"`) {
		t.Fatalf("the creation must name who minted it: %+v", entries)
	}
}

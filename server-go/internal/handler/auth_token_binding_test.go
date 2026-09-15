package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	bindingProjectA = "bind-proj-a"
	bindingProjectB = "bind-proj-b"
	bindingOrgA     = "bind-org-a"
	bindingOrgB     = "bind-org-b"
)

// setupTokenBindingRouter wires AuthHandler with instance + org stores so
// CreateToken can confirm the caller may see the project it binds to. The
// caller is a developer in bindingOrgA only.
func setupTokenBindingRouter(t *testing.T) (chi.Router, *mockTokenStore, string) {
	t.Helper()
	us := newMockUserStore()
	ts := newMockTokenStore()
	user := &domain.User{ID: "bind-u1", Username: testutil.FixtureToken("binder"), Role: "user", Active: true}
	us.users[user.ID] = user

	instances := fakestore.NewInstances()
	instances.Save(&domain.DatabaseInstance{ProjectID: bindingProjectA, OrgID: bindingOrgA})
	instances.Save(&domain.DatabaseInstance{ProjectID: bindingProjectB, OrgID: bindingOrgB})
	orgs := fakestore.NewOrgs()
	orgs.AddMember(bindingOrgA, user.ID, domain.OrgRoleDeveloper)

	raw := testutil.FixtureToken("bind-session")
	hash := auth.HashToken(raw)
	lookup := &fakeLookup{hash: hash, token: &domain.AccessToken{TokenHash: hash, UserID: user.ID}, user: user}

	h := NewAuthHandler(us, ts)
	h.SetOrgStore(orgs)
	h.SetInstanceStore(instances)
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(lookup))
	r.Post(routeAuthTokens, h.CreateToken)
	return r, ts, raw
}

func storedToken(ts *mockTokenStore) *domain.AccessToken {
	for _, tok := range ts.tokens {
		return tok
	}
	return nil
}

func TestAuthCreateToken_ProjectBinding(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		want        int
		wantProject string
		wantScopes  string
	}{
		{"bound to a visible project", `{"name":"ci","projectId":"` + bindingProjectA + `","scopes":["read"]}`, http.StatusCreated, bindingProjectA, "read"},
		{"scopes without binding", `{"name":"ci","scopes":["read","write"]}`, http.StatusCreated, "", "read,write"},
		{"bound to a project in another org", `{"name":"ci","projectId":"` + bindingProjectB + `"}`, http.StatusNotFound, "", ""},
		{"bound to an unknown project", `{"name":"ci","projectId":"ghost"}`, http.StatusNotFound, "", ""},
		{"unknown scope", `{"name":"ci","scopes":["root"]}`, http.StatusBadRequest, "", ""},
		{"bad expiresIn", `{"name":"ci","expiresIn":"soon"}`, http.StatusBadRequest, "", ""},
		{"session scope is reserved", `{"name":"ci","scopes":["session"]}`, http.StatusBadRequest, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ts, raw := setupTokenBindingRouter(t)
			w := doAuthRequest(r, http.MethodPost, routeAuthTokens, raw, tc.body)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want != http.StatusCreated {
				if len(ts.tokens) != 0 {
					t.Error("refused request must not persist a token")
				}
				return
			}
			tok := storedToken(ts)
			if tok == nil || tok.ProjectID != tc.wantProject || tok.Scopes != tc.wantScopes {
				t.Errorf("stored token %+v, want project %q scopes %q", tok, tc.wantProject, tc.wantScopes)
			}
			var resp map[string]interface{}
			json.NewDecoder(w.Body).Decode(&resp)
			if got, _ := resp["projectId"].(string); got != tc.wantProject {
				t.Errorf("response projectId %q want %q", got, tc.wantProject)
			}
		})
	}
}

// Without the instance/org stores wired, binding must fail closed: the
// handler cannot prove the caller may see the project, so it answers 404.
func TestAuthCreateToken_ProjectBindingFailsClosedWithoutStores(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	user := &domain.User{ID: "bind-u2", Username: testutil.FixtureToken("unwired"), Role: "user", Active: true}
	us.users[user.ID] = user
	raw := testutil.FixtureToken("unwired-session")
	hash := auth.HashToken(raw)
	lookup := &fakeLookup{hash: hash, token: &domain.AccessToken{TokenHash: hash, UserID: user.ID}, user: user}

	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(lookup))
	r.Post(routeAuthTokens, NewAuthHandler(us, ts).CreateToken)

	w := doAuthRequest(r, http.MethodPost, routeAuthTokens, raw, `{"name":"ci","projectId":"`+bindingProjectA+`"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404 body=%s", w.Code, w.Body.String())
	}
	if len(ts.tokens) != 0 {
		t.Error("refused request must not persist a token")
	}
}

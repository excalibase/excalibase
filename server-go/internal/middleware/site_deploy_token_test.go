package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	gateRawToken = sites.DeployTokenPrefix + "0123456789abcdef"
	gateProject  = "proj-a"
	gateSite     = "site-a"
	gateCreator  = "user-dev"
)

type fakeDeployTokens struct {
	tokens  map[string]*sites.DeployToken
	err     error
	lookups *int
	used    *[]string
}

func (f fakeDeployTokens) ResolveDeployToken(_ context.Context, raw string) (*sites.DeployToken, error) {
	if f.lookups != nil {
		*f.lookups++
	}
	return f.tokens[raw], f.err
}

func (f fakeDeployTokens) RecordDeployTokenUse(_ context.Context, token *sites.DeployToken) {
	if f.used != nil {
		*f.used = append(*f.used, token.ID)
	}
}

type fakeUsers struct{ users map[string]*domain.User }

func (f fakeUsers) FindByTokenHash(context.Context, string) (*domain.AccessToken, error) {
	return nil, nil
}
func (f fakeUsers) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	return f.users[id], nil
}

type gateSeen struct {
	called      bool
	user        *domain.User
	token       *domain.AccessToken
	deployToken *sites.DeployToken
}

func runDeployTokenGate(t *testing.T, tokens DeployTokenResolver, users auth.TokenLookup, method, path, bearer string) (*httptest.ResponseRecorder, *gateSeen) {
	t.Helper()
	seen := &gateSeen{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.called, seen.user, seen.token = true, auth.GetUser(r.Context()), auth.GetToken(r.Context())
		seen.deployToken = sites.DeployTokenFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	SiteDeployTokenGate(tokens, users)(next).ServeHTTP(recorder, request)
	return recorder, seen
}

func gateFixtures() (fakeDeployTokens, fakeUsers) {
	tokens := fakeDeployTokens{tokens: map[string]*sites.DeployToken{
		gateRawToken: {ID: "tok-1", SiteID: gateSite, ProjectID: gateProject, Name: "ci", CreatedBy: gateCreator},
	}}
	users := fakeUsers{users: map[string]*domain.User{gateCreator: {ID: gateCreator, Role: "user", Active: true}}}
	return tokens, users
}

const sitePrefix = "/api/projects/" + gateProject + "/sites/" + gateSite

// EXC-620: a deploy token reaches its own site's deploy, deploy list and one
// deploy, as its creator narrowed to that project with write scope.
func TestDeployTokenReachesItsOwnSitesDeploys(t *testing.T) {
	tokens, users := gateFixtures()
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, sitePrefix + "/deploy"},
		{http.MethodGet, sitePrefix + "/deploys"},
		{http.MethodGet, sitePrefix + "/deploys/dep-1"},
	} {
		recorder, seen := runDeployTokenGate(t, tokens, users, call.method, call.path, gateRawToken)
		if !seen.called || recorder.Code != http.StatusOK {
			t.Fatalf("%s %s: refused %d %s", call.method, call.path, recorder.Code, recorder.Body)
		}
		if seen.user == nil || seen.user.ID != gateCreator {
			t.Fatalf("%s %s: user = %+v, want the token's creator", call.method, call.path, seen.user)
		}
		if seen.deployToken == nil || seen.deployToken.ID != "tok-1" {
			t.Fatalf("%s %s: the handler is not told which deploy token called", call.method, call.path)
		}
		if seen.token == nil || seen.token.ProjectID != gateProject || !auth.TokenAllowsMethod(seen.token, http.MethodPost) || auth.IsUnrestrictedCredential(seen.token) || auth.IsCapabilityToken(seen.token) {
			t.Fatalf("%s %s: token = %+v, want a write token bound to the project", call.method, call.path, seen.token)
		}
	}
}

// Every other route refuses it, whatever the caller's role would allow.
func TestDeployTokenIsRefusedEverywhereElse(t *testing.T) {
	tokens, users := gateFixtures()
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, sitePrefix},
		{http.MethodGet, sitePrefix + "/"},
		{http.MethodPatch, sitePrefix + "/"},
		{http.MethodDelete, sitePrefix + "/"},
		{http.MethodGet, sitePrefix + "/deploy"},
		{http.MethodPost, sitePrefix + "/deploys"},
		{http.MethodPost, sitePrefix + "/deploys/dep-1/rollback"},
		{http.MethodGet, sitePrefix + "/tokens"},
		{http.MethodPost, sitePrefix + "/tokens"},
		{http.MethodGet, sitePrefix + "/deploys/dep-1/extra"},
		{http.MethodPost, "/api/projects/" + gateProject + "/sites/other-site/deploy"},
		{http.MethodPost, "/api/projects/proj-b/sites/" + gateSite + "/deploy"},
		{http.MethodGet, "/api/projects/" + gateProject + "/sites/"},
		{http.MethodGet, "/api/projects/" + gateProject + "/sites/limits"},
		{http.MethodGet, "/api/provision/" + gateProject},
		{http.MethodGet, "/api/auth/me"},
		{http.MethodPost, "/api/auth/tokens"},
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/healthz"},
		{http.MethodPost, sitePrefix + "/./deploy"},
		{http.MethodPost, sitePrefix + "/deploy/"},
		{http.MethodPost, sitePrefix + "//deploy"},
		{http.MethodHead, sitePrefix + "/deploy"},
		{http.MethodPost, "/api/projects/" + gateProject + "/sites/x/../" + gateSite + "/deploy"},
	} {
		recorder, seen := runDeployTokenGate(t, tokens, users, call.method, call.path, gateRawToken)
		if seen.called || recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), errDeployTokenScope) {
			t.Errorf("%s %s: got %d %s, want 403", call.method, call.path, recorder.Code, recorder.Body)
		}
	}
}

// An escaped path could be authorised as one route and served as another.
func TestDeployTokenRefusesAnEscapedPath(t *testing.T) {
	tokens, users := gateFixtures()
	seen := &gateSeen{}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { seen.called = true })
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+gateProject+"/sites/site%2Da/deploy", nil)
	request.Header.Set("Authorization", "Bearer "+gateRawToken)
	recorder := httptest.NewRecorder()
	SiteDeployTokenGate(tokens, users)(next).ServeHTTP(recorder, request)
	if seen.called || recorder.Code != http.StatusForbidden {
		t.Fatalf("escaped path: %d, called %v", recorder.Code, seen.called)
	}
}

// A revoked or unknown token, or one whose creator is gone, authenticates no one.
func TestDeployTokenThatResolvesToNoOneIsUnauthenticated(t *testing.T) {
	tokens, users := gateFixtures()
	inactive := fakeUsers{users: map[string]*domain.User{gateCreator: {ID: gateCreator, Active: false}}}
	for name, run := range map[string]struct {
		tokens DeployTokenResolver
		users  auth.TokenLookup
		raw    string
	}{
		"unknown":          {tokens, users, sites.DeployTokenPrefix + "revoked"},
		"creator inactive": {tokens, inactive, gateRawToken},
		"creator deleted":  {tokens, fakeUsers{}, gateRawToken},
		"no sites":         {nil, users, gateRawToken},
	} {
		recorder, seen := runDeployTokenGate(t, run.tokens, run.users, http.MethodPost, sitePrefix+"/deploy", run.raw)
		if !seen.called || seen.user != nil || seen.token != nil {
			t.Errorf("%s: %d, called %v user %+v; want it to reach RequireAuth unauthenticated", name, recorder.Code, seen.called, seen.user)
		}
	}
}

// Session cookies and personal access tokens are not this gate's business.
func TestDeployTokenGateLeavesOtherCredentialsAlone(t *testing.T) {
	tokens, users := gateFixtures()
	for _, bearer := range []string{"", "excb_personal"} {
		recorder, seen := runDeployTokenGate(t, tokens, users, http.MethodGet, "/api/auth/me", bearer)
		if !seen.called || recorder.Code != http.StatusOK {
			t.Errorf("bearer %q: %d", bearer, recorder.Code)
		}
	}
}

func TestDeployTokenLookupFailureIsAnInternalError(t *testing.T) {
	_, users := gateFixtures()
	recorder, seen := runDeployTokenGate(t, fakeDeployTokens{err: errors.New("pq: connection refused 10.0.0.1")}, users,
		http.MethodPost, sitePrefix+"/deploy", gateRawToken)
	if seen.called || recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "10.0.0.1") {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body)
	}
}

// Use is recorded only once the token was let through, and a route outside
// its reach is refused before any lookup.
func TestDeployTokenUseIsRecordedOnlyWhenAllowed(t *testing.T) {
	tokens, users := gateFixtures()
	lookups, used := 0, []string{}
	tokens.lookups, tokens.used = &lookups, &used
	runDeployTokenGate(t, tokens, users, http.MethodGet, "/api/auth/me", gateRawToken)
	if lookups != 0 || len(used) != 0 {
		t.Fatalf("a route outside its reach: %d lookups, used %v", lookups, used)
	}
	runDeployTokenGate(t, tokens, users, http.MethodPost, "/api/projects/proj-b/sites/"+gateSite+"/deploy", gateRawToken)
	if len(used) != 0 {
		t.Fatalf("another project's site recorded a use: %v", used)
	}
	runDeployTokenGate(t, tokens, users, http.MethodPost, sitePrefix+"/deploy", gateRawToken)
	if len(used) != 1 || used[0] != "tok-1" {
		t.Fatalf("used = %v, want the allowed deploy recorded", used)
	}
}

// A session cookie never rides along with a deploy token.
func TestDeployTokenNeverRidesOnASession(t *testing.T) {
	_, users := gateFixtures()
	seen := &gateSeen{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.called, seen.user = true, auth.GetUser(r.Context())
	})
	request := httptest.NewRequest(http.MethodPost, sitePrefix+"/deploy", nil)
	request.Header.Set("Authorization", "Bearer "+sites.DeployTokenPrefix+"revoked")
	session := &domain.User{ID: "session-admin", Active: true}
	request = request.WithContext(auth.SetUser(request.Context(), session))
	SiteDeployTokenGate(fakeDeployTokens{}, users)(next).ServeHTTP(httptest.NewRecorder(), request)
	if !seen.called || seen.user != nil {
		t.Fatalf("user = %+v, want no one", seen.user)
	}
}

// The standing Studio shows for a token is what the deploy route enforces:
// the gate, project access and the developer rung, run for real (EXC-620).
func TestDeployTokenStandingMatchesWhatTheDeployRouteEnforces(t *testing.T) {
	for _, creator := range []struct {
		name, role, orgRole string
		active, canDeploy   bool
	}{
		{"developer", "user", domain.OrgRoleDeveloper, true, true},
		{"owner", "user", domain.OrgRoleOwner, true, true},
		{"demoted to viewer", "user", domain.OrgRoleViewer, true, false},
		{"removed from the org", "user", "", true, false},
		{"deactivated developer", "user", domain.OrgRoleDeveloper, false, false},
		{"platform admin outside the org", "platform_admin", "", true, true},
	} {
		t.Run(creator.name, func(t *testing.T) {
			tokens, users := gateFixtures()
			users.users[gateCreator] = &domain.User{ID: gateCreator, Role: creator.role, Active: creator.active}
			instances := fakestore.NewInstances()
			instances.Create(&domain.DatabaseInstance{ProjectID: gateProject, OrgID: testOrg})
			orgs := fakestore.NewOrgs()
			if creator.orgRole != "" {
				orgs.AddMember(testOrg, gateCreator, creator.orgRole)
			}
			standing := DeployTokenStanding{Users: users, Instances: instances, Orgs: orgs}
			canDeploy, err := standing.CreatorCanDeploy(context.Background(), tokens.tokens[gateRawToken])
			if err != nil || canDeploy != creator.canDeploy {
				t.Fatalf("standing = %v, %v; want %v", canDeploy, err, creator.canDeploy)
			}
			if enforced := deployRouteAllows(tokens, users, instances, orgs); enforced != canDeploy {
				t.Fatalf("the route allowed %v but the standing says %v", enforced, canDeploy)
			}
			// A git connection's connector is judged the same way (EXC-354).
			member, err := standing.Standing(context.Background(), gateCreator, gateProject)
			if err != nil || member.CanDeploy != creator.canDeploy {
				t.Fatalf("member standing = %+v, %v; want %v", member, err, creator.canDeploy)
			}
		})
	}
}

// A creator that cannot be read is an error, never a guess either way.
func TestDeployTokenStandingFailsWhenTheCreatorCannotBeRead(t *testing.T) {
	tokens, _ := gateFixtures()
	standing := DeployTokenStanding{Users: failingUsers{}, Instances: fakestore.NewInstances(), Orgs: fakestore.NewOrgs()}
	if _, err := standing.CreatorCanDeploy(context.Background(), tokens.tokens[gateRawToken]); err == nil {
		t.Fatal("an unreadable creator answered a standing")
	}
}

type failingUsers struct{ fakeUsers }

func (failingUsers) FindUserByID(context.Context, string) (*domain.User, error) {
	return nil, errors.New("database down")
}

func deployRouteAllows(tokens fakeDeployTokens, users fakeUsers, instances storage.InstanceStore, orgs storage.OrgStore) bool {
	router := chi.NewRouter()
	router.Use(SiteDeployTokenGate(tokens, users))
	router.With(auth.RequireAuth, RequireProjectAccess(instances, orgs), RequireProjectRole(domain.OrgRoleDeveloper, instances, orgs)).
		Post("/api/projects/{projectId}/sites/{siteId}/deploy", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	request := httptest.NewRequest(http.MethodPost, sitePrefix+"/deploy", nil)
	request.Header.Set("Authorization", "Bearer "+gateRawToken)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Code == http.StatusOK
}

// A connector whose account is gone can deploy nothing, and is named by id.
func TestMemberStandingOfAMissingUser(t *testing.T) {
	_, users := gateFixtures()
	standing := DeployTokenStanding{Users: users, Instances: fakestore.NewInstances(), Orgs: fakestore.NewOrgs()}
	member, err := standing.Standing(context.Background(), "no-such-user", gateProject)
	if err != nil || member.CanDeploy || member.Name != "" {
		t.Fatalf("member %+v %v", member, err)
	}
}

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-11: /internal/email/send is the auth service's only outbound mail path.
// It must be reachable by a service principal holding the email:send
// capability and by nobody else — a studio session or an ordinary PAT driving
// it would let any logged-in user send platform-branded mail to any address.
// These tests drive the REAL router built by buildRouter.

const (
	relayPath = "/internal/email/send"
	relayBody = `{"projectId":"proj-a","to":"user@example.com","template":"verify_email",
		"data":{"userEmail":"user@example.com","verifyUrl":"https://app.example.io/verify?token=abc"}}`

	relayCallerAnonymous  = "anonymous"
	relayCallerUnknown    = "unknownToken"
	relayCallerSession    = "studioSession"
	relayCallerAdminPAT   = "platformAdminPAT"
	relayCallerOtherSvc   = "svcGraphql"
	relayCallerAuthSvc    = "svcAuth"
	relayAdminUserID      = "admin-1"
	relayServiceUserID    = "svc-auth-1"
	relayOtherServiceUser = "svc-graphql-1"
)

// countingSender records how many messages actually reached a provider, so a
// refused request can be told apart from an accepted-and-dropped one.
type countingSender struct{ sent int }

func (s *countingSender) Send(_ context.Context, _ email.Message) (string, error) {
	s.sent++
	return "msg-1", nil
}

// relayRouter builds the production router with the mail relay wired, and
// returns the raw bearer token of each caller class.
func relayRouter(t *testing.T, sender email.Sender) (http.Handler, callers) {
	t.Helper()
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.Users[relayAdminUserID] = &domain.User{ID: relayAdminUserID, Role: "platform_admin", Active: true}
	platform.Users[relayServiceUserID] = &domain.User{ID: relayServiceUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	platform.Users[relayOtherServiceUser] = &domain.User{ID: relayOtherServiceUser, Role: "platform_admin", Active: true, Kind: domain.UserKindService}

	who := callers{relayCallerAnonymous: "", relayCallerUnknown: testutil.FixtureToken(relayCallerUnknown)}
	issue := func(name, userID string, tok domain.AccessToken) {
		raw := testutil.FixtureToken(name)
		tok.TokenHash = auth.HashToken(raw)
		tok.UserID = userID
		platform.ByHash[tok.TokenHash] = &tok
		who[name] = raw
	}
	issue(relayCallerSession, relayAdminUserID, domain.AccessToken{Scopes: auth.ScopeSession})
	issue(relayCallerAdminPAT, relayAdminUserID, domain.AccessToken{Name: "ci"})
	issue(relayCallerOtherSvc, relayOtherServiceUser, domain.AccessToken{
		Name: "svc-graphql", Permissions: []string{"projects:info:read", "policies:read"}})
	issue(relayCallerAuthSvc, relayServiceUserID, domain.AccessToken{
		Name: "svc-auth", Permissions: []string{"vault:read:pki/signing/*", "projects:info:read", "email:send"}})

	deps := matrixDeps(t, instances)
	deps.internalEmail = handler.NewInternalEmailHandler(sender)
	cfg := config.AppConfig{DeploymentMode: "selfhosted"}
	return buildRouter(cfg, platform, instances, deps), who
}

func postRelay(router http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, relayPath, strings.NewReader(relayBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestEmailRelayRejectsEveryCallerButTheGrantedService(t *testing.T) {
	cases := []struct {
		caller string
		code   int
	}{
		{relayCallerAnonymous, http.StatusUnauthorized},
		{relayCallerUnknown, http.StatusUnauthorized},
		{relayCallerSession, http.StatusForbidden},
		{relayCallerAdminPAT, http.StatusForbidden},
		{relayCallerOtherSvc, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.caller, func(t *testing.T) {
			sender := &countingSender{}
			router, who := relayRouter(t, sender)
			w := postRelay(router, who[tc.caller])
			if w.Code != tc.code {
				t.Fatalf("want %d, got %d body=%s", tc.code, w.Code, w.Body.String())
			}
			if sender.sent != 0 {
				t.Fatalf("a refused caller reached the provider %d times", sender.sent)
			}
		})
	}
}

func TestEmailRelayAcceptsTheAuthServiceToken(t *testing.T) {
	sender := &countingSender{}
	router, who := relayRouter(t, sender)

	w := postRelay(router, who[relayCallerAuthSvc])

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", w.Code, w.Body.String())
	}
	if sender.sent != 1 {
		t.Fatalf("provider received %d messages, want 1", sender.sent)
	}
}

// Without a provider the relay must say so rather than silently swallow mail
// the auth service believes it sent.
func TestEmailRelayWithoutAProviderIsUnavailable(t *testing.T) {
	router, who := relayRouter(t, nil)

	w := postRelay(router, who[relayCallerAuthSvc])

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

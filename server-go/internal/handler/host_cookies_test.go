package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	hostSessionName   = "__Host-excali_session"
	legacySessionName = "excali_session"
	callbackPath      = "/api/auth/oauth/github/callback?code=c&state=st4te"
)

func setCookies(w *httptest.ResponseRecorder) map[string][]*http.Cookie {
	byName := map[string][]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		byName[c.Name] = append(byName[c.Name], c)
	}
	return byName
}

func requireHostAttributes(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly {
		t.Fatalf("cookie lacks the __Host- attributes: %+v", c)
	}
}

func requireLegacyCleared(t *testing.T, cookies map[string][]*http.Cookie) {
	t.Helper()
	legacy := cookies[legacySessionName]
	if len(legacy) != 1 || legacy[0].Value != "" || legacy[0].MaxAge >= 0 || legacy[0].Path != "/" {
		t.Fatalf("the unprefixed session cookie was not cleared: %+v", legacy)
	}
}

func TestLoginSetsAHostSessionAndClearsTheLegacyCookie(t *testing.T) {
	users := newMockUserStore()
	password := testutil.FixturePassword("host-cookie")
	now := time.Now()
	users.users["u1"] = &domain.User{ID: "u1", Username: "alice", PasswordHash: testutil.FixtureVerifiableHash(password),
		Active: true, EmailVerifiedAt: &now, CreatedAt: &now}
	router := setupAuthRouter(t, users, newMockTokenStore())

	w := doRequest(router, "POST", routeLogin, fmt.Sprintf(`{"username":"alice","password":%q}`, password))
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := setCookies(w)
	session := cookies[hostSessionName]
	if len(session) != 1 || session[0].Value == "" {
		t.Fatalf("no __Host- session: %v", cookies)
	}
	requireHostAttributes(t, session[0])
	requireLegacyCleared(t, cookies)
}

func TestLogoutClearsBothSessionCookies(t *testing.T) {
	w := doRequest(setupAuthRouter(t, newMockUserStore(), newMockTokenStore()), "POST", "/api/auth/logout", "")
	cookies := setCookies(w)
	host := cookies[hostSessionName]
	if len(host) != 1 || host[0].Value != "" || host[0].MaxAge >= 0 {
		t.Fatalf("the __Host- session was not cleared: %+v", host)
	}
	requireHostAttributes(t, host[0])
	requireLegacyCleared(t, cookies)
}

func TestOAuthStateCookieIsHostPrefixed(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := doRequest(h.router, "GET", "/api/auth/oauth/github/start", "")
	state := setCookies(w)["__Host-excali_oauth_state"]
	if len(state) != 1 {
		t.Fatalf("no __Host- state cookie: %v", w.Result().Cookies())
	}
	requireHostAttributes(t, state[0])
}

func TestOAuthPlantedStateCookieCannotCompleteTheFlow(t *testing.T) {
	h := newOAuthHarness(t, false)
	for name, cookies := range map[string][]*http.Cookie{
		"unprefixed only": {{Name: "excali_oauth_state", Value: "st4te"}},
		"two host cookies": {
			{Name: "__Host-excali_oauth_state", Value: "st4te"},
			{Name: "__Host-excali_oauth_state", Value: "st4te"},
		},
	} {
		req := httptest.NewRequest("GET", callbackPath, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.router.ServeHTTP(w, req)
		refusal := redirectedTo(t, w).Query().Get("oauth_error")
		if refusal == "" {
			t.Errorf("%s: the flow completed", name)
		}
		if sessionCookie(w) != nil {
			t.Errorf("%s: a session was issued", name)
		}
	}
}

func TestSiteGitCookieIsHostPrefixed(t *testing.T) {
	requireHostAttributes(t, siteGitCookie("key", 600))
}

func TestInsecureCookiesDropThePrefixAndSecureForHTTPInstalls(t *testing.T) {
	auth.UseInsecureCookies(true)
	t.Cleanup(func() { auth.UseInsecureCookies(false) })
	w := httptest.NewRecorder()
	writeSessionCookie(w, "raw", time.Now().Add(time.Hour))
	cookies := setCookies(w)
	if got := cookies[legacySessionName]; len(got) != 1 || got[0].Secure || got[0].Value != "raw" || !got[0].HttpOnly {
		t.Fatalf("insecure session cookie: %+v", got)
	}
	if len(cookies[hostSessionName]) != 0 {
		t.Fatal("the prefixed name must not be set in insecure mode")
	}
}

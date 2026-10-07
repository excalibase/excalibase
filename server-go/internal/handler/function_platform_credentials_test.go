package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// EXC-563: tenant function code must never receive a platform credential.
// The Studio session cookie (and the sign-in state cookie) are stripped by
// name on every route into the runtime, the customer's own cookies kept; a
// platform PAT in Authorization and the runtime token never reach user code.

const platformPAT = "excb_0123456789abcdef0123456789abcdef"

func TestPublicInvoke_StripsPlatformCookiesAndKeepsTheCustomersOwn(t *testing.T) {
	r, seen, sign := newMarkerTestRouter(t)
	cookie := auth.SessionCookieName + "=" + platformPAT + "; theme=dark; " + oauthStateCookie + "=s1; cart=42"

	for _, call := range []struct{ fn, token string }{{"open", ""}, {"secure", sign(nil)}} {
		invokeMarker(r, call.fn, call.token, map[string]string{"Cookie": cookie})

		got := (*seen)["Cookie"]
		if strings.Contains(got, auth.SessionCookieName) || strings.Contains(got, oauthStateCookie) || strings.Contains(got, platformPAT) {
			t.Errorf("%s: a platform cookie reached tenant code: %q", call.fn, got)
		}
		if got != "theme=dark; cart=42" {
			t.Errorf("%s: customer cookies = %q, want %q", call.fn, got, "theme=dark; cart=42")
		}
	}
}

func TestPublicInvoke_DropsTheCookieHeaderWhenOnlyPlatformCookiesWereSent(t *testing.T) {
	r, seen, _ := newMarkerTestRouter(t)

	invokeMarker(r, "open", "", map[string]string{"Cookie": auth.SessionCookieName + "=" + platformPAT})

	if got, present := (*seen)["Cookie"]; present {
		t.Errorf("Cookie forwarded as %q, want no header", got)
	}
}

func TestPublicInvoke_NeverForwardsAPlatformPAT(t *testing.T) {
	r, seen, _ := newMarkerTestRouter(t)

	invokeMarker(r, "open", platformPAT, nil)

	if got := (*seen)["Authorization"]; got != "" {
		t.Errorf("a platform PAT reached tenant code: Authorization=%q", got)
	}
}

func TestPublicInvoke_KeepsTheCallersOwnAuthorization(t *testing.T) {
	r, seen, sign := newMarkerTestRouter(t)
	token := sign(nil)

	invokeMarker(r, "open", "customer-api-key", nil)
	if got := (*seen)["Authorization"]; got != sharedBearerPrefix+"customer-api-key" {
		t.Errorf("unverified function: Authorization=%q, want the caller's own", got)
	}
	invokeMarker(r, "secure", token, nil)
	if got := (*seen)["Authorization"]; got != sharedBearerPrefix+token {
		t.Errorf("verified function: Authorization=%q, want the end-user token", got)
	}
}

func TestRuntimeHeaders_StripPlatformCredentialsOnEveryAuthMode(t *testing.T) {
	for _, mode := range []callerAuth{authStripped, authUnverified, authVerified} {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Header.Set(runtimeTokenHeader, "derived-runtime-secret")
		req.Header.Set("Authorization", "Bearer "+platformPAT)
		req.Header.Add("Cookie", auth.SessionCookieName+"="+platformPAT)
		req.Header.Add("Cookie", "theme=dark") // HTTP/2 may split cookies across header lines
		req.Header.Set(authVerifiedHeader, "1")

		headers := runtimeHeaders(req, mode)

		if _, present := headers[runtimeTokenHeader]; present {
			t.Errorf("mode %d: runtime token forwarded", mode)
		}
		if got := headers["Authorization"]; got != "" {
			t.Errorf("mode %d: Authorization=%q, want none", mode, got)
		}
		if mode == authStripped {
			if _, present := headers["Cookie"]; present {
				t.Errorf("Studio test-run: Cookie forwarded")
			}
		} else if got := headers["Cookie"]; got != "theme=dark" {
			t.Errorf("mode %d: Cookie=%q, want theme=dark", mode, got)
		}
		if got := headers[authVerifiedHeader]; (mode == authVerified) != (got == "1") {
			t.Errorf("mode %d: %s=%q", mode, authVerifiedHeader, got)
		}
	}
}

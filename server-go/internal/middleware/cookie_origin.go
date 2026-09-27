package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// TrustedOrigins are the pages allowed to act with the Studio session cookie:
// Studio itself and any origin CORS names explicitly. The wildcard grants
// cross-origin calls, never the cookie.
func TrustedOrigins(studioURL string, corsOrigins []string) []string {
	var origins []string
	for _, o := range append([]string{studioURL}, corsOrigins...) {
		if o != "*" && o != "" {
			origins = append(origins, strings.TrimRight(o, "/"))
		}
	}
	return origins
}

// RequireTrustedOriginForCookies refuses a state-changing request that the
// session cookie would authenticate unless the page that sent it is trusted.
// SameSite alone admits sibling subdomains, such as hosted customer apps. A
// request carrying a bearer token is not cookie-authenticated and passes: a
// foreign page cannot set that header without a CORS preflight.
func RequireTrustedOriginForCookies(trusted []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(trusted))
	for _, o := range trusted {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cookieAuthenticatedWrite(r) && !trustedOrigin(allowed, requestOrigin(r)) {
				http.Error(w, `{"error":"cross-origin request refused"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func trustedOrigin(allowed map[string]bool, origin string) bool {
	return origin != "" && allowed[origin]
}

func cookieAuthenticatedWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	c, err := r.Cookie(auth.SessionCookieName)
	return err == nil && c.Value != ""
}

// requestOrigin is the Origin header, or the origin of the Referer when a
// browser left Origin out. Neither yields "".
func requestOrigin(r *http.Request) string {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin
	}
	ref, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || ref.Scheme == "" || ref.Host == "" {
		return ""
	}
	return ref.Scheme + "://" + ref.Host
}

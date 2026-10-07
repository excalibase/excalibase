package handler

import (
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// platformCookies are the cookies the platform sets on its own host. Tenant
// code must never see them: the session cookie holds the developer's PAT.
var platformCookies = map[string]bool{
	auth.SessionCookieName: true,
	oauthStateCookie:       true,
}

// platformTokenPrefix marks a platform PAT (Studio sessions, service tokens).
const platformTokenPrefix = "excb_"

// runtimeHeaders is the request's headers as tenant code receives them. On
// every route the platform's own credentials are removed — its cookies by
// name, a platform PAT in Authorization, the runtime token and the gateway's
// verification marker — while the customer's own cookies and tokens pass.
func runtimeHeaders(r *http.Request, mode callerAuth) map[string]string {
	headers := make(map[string]string, len(r.Header))
	for name, values := range r.Header {
		if len(values) > 0 && !strings.EqualFold(name, authVerifiedHeader) && !strings.EqualFold(name, runtimeTokenHeader) {
			headers[name] = values[0]
		}
	}
	if isPlatformToken(headers["Authorization"]) {
		delete(headers, "Authorization")
	}
	if customer := customerCookies(r); customer != "" {
		headers["Cookie"] = customer
	} else {
		delete(headers, "Cookie")
	}
	switch mode {
	case authStripped:
		delete(headers, "Authorization")
		delete(headers, "Cookie")
	case authVerified:
		headers[authVerifiedHeader] = "1"
	}
	return headers
}

func isPlatformToken(authorization string) bool {
	token, found := strings.CutPrefix(authorization, "Bearer ")
	return found && strings.HasPrefix(strings.TrimSpace(token), platformTokenPrefix)
}

// customerCookies rebuilds the Cookie header without the platform's cookies,
// across every Cookie line the request carried.
func customerCookies(r *http.Request) string {
	var kept []string
	for _, line := range r.Header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			name, _, _ := strings.Cut(pair, "=")
			if !platformCookies[strings.TrimSpace(name)] {
				kept = append(kept, pair)
			}
		}
	}
	return strings.Join(kept, "; ")
}

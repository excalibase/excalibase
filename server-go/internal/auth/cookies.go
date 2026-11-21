package auth

import (
	"net/http"
	"sync/atomic"
)

// HostCookiePrefix makes a browser keep a cookie Secure, Path=/ and without a
// Domain attribute, so a sibling subdomain cannot plant or replace it.
const HostCookiePrefix = "__Host-"

// LegacySessionCookieName is the session cookie before the prefix; it is
// cleared on sign-in and sign-out and never authenticates.
const LegacySessionCookieName = "excali_session"

var insecureCookies atomic.Bool

// UseInsecureCookies drops the __Host- prefix and the Secure flag so Studio can
// sign in over plain http on a LAN address. It is set once at start-up and is
// refused on an https install.
func UseInsecureCookies(on bool) { insecureCookies.Store(on) }

// SecureCookies is whether cookies carry the Secure flag.
func SecureCookies() bool { return !insecureCookies.Load() }

// CookieName is the name the server sets and reads for a base cookie name.
func CookieName(base string) string {
	if insecureCookies.Load() {
		return base
	}
	return HostCookiePrefix + base
}

// NewCookie is an HttpOnly cookie with the attributes __Host- demands.
func NewCookie(base, value string, maxAge int, sameSite http.SameSite) *http.Cookie {
	return &http.Cookie{
		Name: CookieName(base), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: SecureCookies(), SameSite: sameSite,
	}
}

// ReadCookie returns the value of the one cookie with this base name. A missing
// cookie, or several carrying the name, reads as absent: which one is genuine
// cannot be told.
func ReadCookie(r *http.Request, base string) (string, bool) {
	name := CookieName(base)
	value, found := "", 0
	for _, c := range r.Cookies() {
		if c.Name == name {
			value = c.Value
			found++
		}
	}
	if found != 1 || value == "" {
		return "", false
	}
	return value, true
}

// HasCookie is whether the request carries any cookie with this base name,
// however many.
func HasCookie(r *http.Request, base string) bool {
	name := CookieName(base)
	for _, c := range r.Cookies() {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

// ClearLegacyCookie expires the unprefixed cookie an earlier release set. It
// does nothing when the unprefixed name is the live one.
func ClearLegacyCookie(w http.ResponseWriter, base string) {
	if insecureCookies.Load() {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: base, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

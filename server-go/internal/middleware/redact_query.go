package middleware

import (
	"net/http"
	"net/url"
)

// RedactQueryParams replaces the named query values in RequestURI, which is
// what the access log prints, so read-only SQL never lands in a log line.
// Handlers read r.URL, which keeps the values.
func RedactQueryParams(names ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			redacted := false
			for _, name := range names {
				if query.Has(name) {
					query.Set(name, "REDACTED")
					redacted = true
				}
			}
			if redacted {
				r = r.Clone(r.Context())
				r.RequestURI = (&url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: query.Encode()}).RequestURI()
			}
			next.ServeHTTP(w, r)
		})
	}
}

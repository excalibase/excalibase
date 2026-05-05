package middleware

import "net/http"

// SecurityHeaders sets standard security response headers on all responses.
//
// CSP: `default-src 'self'` blocks any cross-origin script/style/image. The
// React studio is served from the same origin as the API, so this is the
// tightest setting that still works. If embedded inside an iframe (future)
// or third-party CDNs are introduced, relax per-directive rather than
// loosening default-src.
//
// HSTS: 2-year max-age + includeSubDomains. Only meaningful on TLS, but
// emitted unconditionally — browsers ignore it on HTTP and the platform
// must always be deployed behind TLS in production.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+ // CodeMirror/Monaco inject inline styles
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

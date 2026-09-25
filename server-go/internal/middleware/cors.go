package middleware

import (
	"net/http"
	"strings"
)

var (
	corsAllowMethods = strings.Join([]string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"}, ", ")
	corsAllowHeaders = strings.Join([]string{"Authorization", "Content-Type", "X-Request-ID", "X-CSRF-Token", "If-Match"}, ", ")
)

// CORS returns middleware that validates Origin against allowed origins
// and sets appropriate CORS response headers.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	wildcard := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"
	originSet := buildOriginSet(allowedOrigins)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			originAllowed := origin != "" && (wildcard || originSet[origin])

			if originAllowed {
				setCORSOriginHeaders(w, origin)
			}

			if r.Method == http.MethodOptions {
				handlePreflight(w, originAllowed)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func buildOriginSet(origins []string) map[string]bool {
	set := make(map[string]bool, len(origins))
	for _, o := range origins {
		set[o] = true
	}
	return set
}

func setCORSOriginHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Vary", "Origin")
}

func handlePreflight(w http.ResponseWriter, originAllowed bool) {
	if originAllowed {
		w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Max-Age", "3600")
	}
	w.WriteHeader(http.StatusNoContent)
}

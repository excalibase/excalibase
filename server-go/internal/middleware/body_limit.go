package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Request body caps for JSON routes (EXC-555). Routes that take uploads keep
// their own, larger limits.
const (
	// DefaultBodyLimit is the cap for ordinary JSON routes.
	DefaultBodyLimit int64 = 1 << 20
	// AuthBodyLimit is the cap for the unauthenticated sign-in routes.
	AuthBodyLimit int64 = 64 << 10
	// SchemaBodyLimit is the cap for DDL, SQL and row writes.
	SchemaBodyLimit int64 = 4 << 20
)

// LimitBody refuses a request body larger than limit bytes with 413 before
// the handler runs, so every handler behind it sees a bounded body and an
// oversize is never reported as a malformed one.
func LimitBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				refuseOversizedBody(w, limit)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			if err != nil {
				writeBodyError(w, http.StatusBadRequest, "could not read the request body")
				return
			}
			if int64(len(body)) > limit {
				refuseOversizedBody(w, limit)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

func refuseOversizedBody(w http.ResponseWriter, limit int64) {
	writeBodyError(w, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("request body is too large: the limit is %d bytes", limit))
}

func writeBodyError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": msg, "status": status})
}

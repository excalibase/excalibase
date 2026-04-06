package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// validPathID allows only alphanumeric, hyphens, underscores, max 64 chars.
// Used to validate orgId and projectId path parameters to prevent path traversal.
var validPathID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

func isValidID(id string) bool {
	return validPathID.MatchString(id)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":  msg,
		"status": code,
	})
}

// safeError returns a sanitized error message safe for client responses.
// It strips PG internals and truncates to 200 chars.
func safeError(err error) string {
	msg := err.Error()
	// Strip common PG detail prefixes that leak schema info
	for _, prefix := range []string{"pq: ", "ERROR: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	// Remove DETAIL/HINT lines
	if idx := strings.Index(msg, "\nDETAIL:"); idx >= 0 {
		msg = msg[:idx]
	}
	if idx := strings.Index(msg, "\nHINT:"); idx >= 0 {
		msg = msg[:idx]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// schemaError logs the full error server-side and returns a sanitized message.
func schemaError(w http.ResponseWriter, err error, code int) {
	log.Printf("schema error: %v", err)
	httpError(w, safeError(err), code)
}

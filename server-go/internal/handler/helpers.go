package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// validPathID allows only alphanumeric, hyphens, underscores, max 64 chars.
// Used to validate orgId and projectId path parameters to prevent path traversal.
var validPathID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// validSlug allows lowercase alphanumeric and hyphens, 2-50 chars.
var validSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{1,49}$`)

func isValidID(id string) bool {
	return validPathID.MatchString(id)
}

// validEmail basic email format check
var validEmail = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func isValidEmail(email string) bool {
	return validEmail.MatchString(email)
}

func isValidPassword(password string) string {
	if len(password) < 8 {
		return "password must be at least 8 characters"
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	for _, c := range password {
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return "password must contain uppercase, lowercase, and a digit"
	}
	return ""
}

func isValidSlug(slug string) bool {
	return validSlug.MatchString(slug)
}

// logSanitizer strips the line terminators an attacker would use to forge
// extra log records out of a value that came from a request.
var logSanitizer = strings.NewReplacer("\n", "", "\r", "")

// safeLog makes a request-derived value safe to write to the log.
func safeLog(s string) string {
	return logSanitizer.Replace(s)
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

// refuseWhileDeleting writes 409 and reports true when the project is being
// torn down. Routes that carry a project id but sit outside
// RequireProjectAccess call it themselves — the gate cannot see them.
func refuseWhileDeleting(w http.ResponseWriter, instances storage.InstanceStore, projectID string) bool {
	if instances == nil {
		return false
	}
	inst, err := instances.FindByProjectID(projectID)
	if err != nil || inst == nil || !domain.IsDeletionStatus(inst.Status) {
		return false
	}
	httpError(w, "project is being deleted", http.StatusConflict)
	return true
}

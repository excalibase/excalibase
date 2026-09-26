package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
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

// writeProjectCreationError answers the refusals every path that creates a
// project shares, and reports whether it wrote the response. A full
// organisation conflicts with the caller's current state (409) and is told the
// fixed refusal; a platform database that could not answer is ours to own
// (500) and the caller learns nothing more than that. Anything else is left to
// the caller's own mapping.
func writeProjectCreationError(w http.ResponseWriter, err error) bool {
	var limitErr *service.OrgProjectLimitError
	switch {
	case errors.As(err, &limitErr):
		httpError(w, limitErr.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrProjectStoreUnavailable):
		log.Printf("project creation refused: %v", err)
		httpError(w, service.ErrProjectStoreUnavailable.Error(), http.StatusInternalServerError)
	case errors.Is(err, service.ErrOrgTierUnresolved):
		log.Printf("project creation refused: %v", err)
		httpError(w, service.ErrOrgTierUnresolved.Error(), http.StatusInternalServerError)
	default:
		return false
	}
	return true
}

// schemaError logs the full error server-side and returns a sanitized message.
func schemaError(w http.ResponseWriter, err error, code int) {
	log.Printf("schema error: %v", err)
	httpError(w, safeError(err), code)
}

// refuseWhileNotServable writes 409 and reports true when the project must
// not be served — under teardown, or restored but not yet confirmed. Routes
// that carry a project id but sit outside RequireProjectAccess call it
// themselves: the gate cannot see them.
func refuseWhileNotServable(w http.ResponseWriter, instances storage.InstanceStore, projectID string) bool {
	if instances == nil {
		return false
	}
	inst, err := instances.FindByProjectID(projectID)
	if err != nil || inst == nil || !domain.IsNotServable(inst.Status) {
		return false
	}
	httpError(w, domain.NotServableReason(inst.Status), http.StatusConflict)
	return true
}

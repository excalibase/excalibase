package handler

import (
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
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

// validUsername is the rule for a new account's name; Studio's sign-up form
// uses the same pattern. Accounts named before the rule keep signing in.
var validUsername = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

const usernameRule = "Use 3–32 letters, numbers or underscores"

func isValidUsername(username string) bool {
	return validUsername.MatchString(username)
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

func writeJSONStatus(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
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
	// The driver prefix also appears after our own wrapping ("create table: pq: ...").
	msg = strings.ReplaceAll(msg, "pq: ", "")
	msg = strings.TrimPrefix(msg, "ERROR: ")
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
// errBackupsUnavailable is all a caller learns when its project's backup
// credentials could not be issued; the cause is in the log.
const errBackupsUnavailable = "backups cannot be set up right now; try again later"

func writeProjectCreationError(w http.ResponseWriter, err error) bool {
	var limitErr *service.OrgProjectLimitError
	switch {
	case errors.As(err, &limitErr):
		httpError(w, limitErr.Error(), http.StatusConflict)
	case errors.Is(err, storagebudget.ErrExceeded):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrDocumentDBNotInstalled):
		httpError(w, service.ErrDocumentDBNotInstalled.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrProjectStoreUnavailable):
		log.Printf("project creation refused: %v", err)
		httpError(w, service.ErrProjectStoreUnavailable.Error(), http.StatusInternalServerError)
	case errors.Is(err, service.ErrOrgTierUnresolved):
		log.Printf("project creation refused: %v", err)
		httpError(w, service.ErrOrgTierUnresolved.Error(), http.StatusInternalServerError)
	case errors.Is(err, service.ErrBackupTargetNotConfigured):
		log.Printf("project creation refused: %v", err)
		httpError(w, service.ErrBackupTargetNotConfigured.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, service.ErrBackupCredentialsNotConfigured),
		errors.Is(err, service.ErrBackupCredentialsUnavailable),
		errors.Is(err, service.ErrBackupPrefixInUse):
		log.Printf("project creation refused: %v", err)
		httpError(w, errBackupsUnavailable, http.StatusServiceUnavailable)
	default:
		return writeNodePlacementError(w, err)
	}
	return true
}

// schemaError logs the full error server-side and returns a sanitized message.
// A Postgres refusal is answered with Postgres's own message (no DETAIL/HINT)
// and, when the caller passed the 500 default, a status saying whose mistake
// it was: a duplicate is 409, a bad statement or value 400, a missing grant 403.
func schemaError(w http.ResponseWriter, err error, code int) {
	log.Printf("schema error: %v", err)
	var nameErr *schema.InvalidNameError
	if errors.As(err, &nameErr) {
		httpError(w, nameErr.Error(), http.StatusBadRequest)
		return
	}
	var inputErr *schema.InputError
	if errors.As(err, &inputErr) {
		httpError(w, truncate(err.Error(), 200), http.StatusBadRequest)
		return
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Message != "" {
		if code == http.StatusInternalServerError {
			code = postgresRefusalStatus(pqErr.Code)
		}
		httpError(w, truncate(pqErr.Message, 200), code)
		return
	}
	httpError(w, safeError(err), code)
}

func postgresRefusalStatus(code pq.ErrorCode) int {
	switch code {
	case "42P07", "42701", "42710", "42P06", "42723", "42P04", "23505":
		return http.StatusConflict
	case "42501":
		return http.StatusForbidden
	}
	switch code.Class() {
	case "22", "23", "42", "2B", "0A":
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
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

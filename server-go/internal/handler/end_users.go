package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/endusers"
)

const (
	auditActionEndUserRoleSet = "end_user.role.set"
	auditResourceEndUser      = "end_user"

	maxEndUserPageLimit   = 1000
	maxEndUserPageOffset  = 1 << 31
	maxAllowedRoles       = 32
	maxRoleChangeBodySize = 16 << 10
	maxAuditedUserIDRunes = 64

	codeInvalidRequest         = "invalid_request"
	codeAuthUnavailable        = "auth_unavailable"
	codeAuthRejectedCredential = "auth_rejected_credential"
	codeAuthNotConfigured      = "auth_not_configured"
	codeInternalError          = "internal_error"

	outcomeSuccess         = "success"
	outcomeRefused         = "refused"
	outcomeInvalidRequest  = "invalid_request"
	outcomeAuthUnavailable = "auth_unavailable"
	outcomeError           = "error"
)

// EndUserManager is excalibase-auth's end-user routes seen from the control plane.
type EndUserManager interface {
	List(ctx context.Context, orgSlug, projectID, actor string, page endusers.Page) (json.RawMessage, error)
	SetRole(ctx context.Context, orgSlug, projectID, actor string, userID int64, change endusers.RoleChange) (*endusers.EndUser, error)
}

// EndUsersHandler lets a project admin list the project's end users and set
// their role (EXC-370). Auth stores the accounts; this handler relays the
// request with a user-admin token naming the admin, and audits every change.
type EndUsersHandler struct {
	users     EndUserManager
	instances ProjectFinder
	orgs      OrgFinder
	audit     auditWriter
}

func NewEndUsersHandler(users EndUserManager, instances ProjectFinder, orgs OrgFinder, audit auditWriter) *EndUsersHandler {
	return &EndUsersHandler{users: users, instances: instances, orgs: orgs, audit: audit}
}

func (h *EndUsersHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Put("/{userId}/role", h.SetRole)
}

func (h *EndUsersHandler) List(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolve(w, r)
	if !ok {
		return
	}
	page, err := parseEndUserPage(r)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	listed, err := h.users.List(r.Context(), target.orgSlug, target.projectID, target.actor, page)
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(listed)
}

func (h *EndUsersHandler) SetRole(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolve(w, r)
	if !ok {
		return
	}
	rawUserID := chi.URLParam(r, "userId")
	userID, change, err := parseRoleChange(w, r, rawUserID)
	if err != nil {
		h.recordRoleChange(r, target, map[string]any{"userId": truncateRunes(rawUserID, maxAuditedUserIDRunes), "outcome": outcomeInvalidRequest})
		writeCodedError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	user, err := h.users.SetRole(r.Context(), target.orgSlug, target.projectID, target.actor, userID, change)
	h.recordRoleChange(r, target, roleChangeAudit(userID, change, err))
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, user)
}

type endUserTarget struct {
	projectID, orgSlug, actor string
}

// resolve names the project, its organisation's slug (auth routes by it) and
// the platform user the token will name as actor.
func (h *EndUsersHandler) resolve(w http.ResponseWriter, r *http.Request) (endUserTarget, bool) {
	if h.users == nil {
		writeCodedError(w, http.StatusServiceUnavailable, codeAuthNotConfigured,
			"end-user management is unavailable: the auth service is not configured")
		return endUserTarget{}, false
	}
	user := auth.GetUser(r.Context())
	if user == nil || user.ID == "" {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return endUserTarget{}, false
	}
	projectID := chi.URLParam(r, "projectId")
	inst, err := h.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return endUserTarget{}, false
	}
	org, err := h.orgs.FindOrgByID(r.Context(), inst.OrgID)
	if err != nil || org == nil || org.Slug == "" {
		log.Printf("ERROR: end users: organisation %q of %s not found: %v", inst.OrgID, projectID, err)
		httpError(w, "the project's organisation could not be resolved", http.StatusInternalServerError)
		return endUserTarget{}, false
	}
	return endUserTarget{projectID: projectID, orgSlug: org.Slug, actor: user.ID}, true
}

func parseEndUserPage(r *http.Request) (endusers.Page, error) {
	query := r.URL.Query()
	limit, err := optionalBoundedInt(query.Get("limit"), 1, maxEndUserPageLimit)
	if err != nil {
		return endusers.Page{}, errors.New("limit must be a whole number from 1 to 1000")
	}
	offset, err := optionalBoundedInt(query.Get("offset"), 0, maxEndUserPageOffset)
	if err != nil {
		return endusers.Page{}, errors.New("offset must be a whole number of at least 0")
	}
	return endusers.Page{Limit: limit, Offset: offset}, nil
}

func optionalBoundedInt(raw string, lowest, highest int) (*int, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < lowest || value > highest {
		return nil, errors.New("out of range")
	}
	return &value, nil
}

var endUserRoleName = regexp.MustCompile(domain.GrantRolePattern)

var (
	errRoleChangeUserID = errors.New("userId must be a positive whole number")
	errRoleChangeBody   = errors.New(`body must be {"role": string, "allowedRoles"?: [string]}`)
	errRoleChangeRole   = errors.New(`role must be a role name (lower-case letters, digits and underscores, starting with a letter, at most 63 characters), not "service"`)
	errRoleChangeList   = errors.New("allowedRoles must list distinct role names, at most 32, including role")
)

func parseRoleChange(w http.ResponseWriter, r *http.Request, rawUserID string) (int64, endusers.RoleChange, error) {
	userID, err := strconv.ParseInt(rawUserID, 10, 64)
	if err != nil || userID <= 0 {
		return 0, endusers.RoleChange{}, errRoleChangeUserID
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRoleChangeBodySize))
	decoder.DisallowUnknownFields()
	var change endusers.RoleChange
	if err := decoder.Decode(&change); err != nil {
		return 0, endusers.RoleChange{}, errRoleChangeBody
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, endusers.RoleChange{}, errRoleChangeBody
	}
	if err := validateRoleChange(change); err != nil {
		return 0, endusers.RoleChange{}, err
	}
	return userID, change, nil
}

// validateRoleChange checks shape only; which roles exist is auth's call.
// "service" bypasses exposure entirely, so no end user may be given it.
func validateRoleChange(change endusers.RoleChange) error {
	if !isAssignableRole(change.Role) {
		return errRoleChangeRole
	}
	if change.AllowedRoles == nil {
		return nil
	}
	if len(change.AllowedRoles) == 0 || len(change.AllowedRoles) > maxAllowedRoles {
		return errRoleChangeList
	}
	seen := make(map[string]bool, len(change.AllowedRoles))
	for _, role := range change.AllowedRoles {
		if !isAssignableRole(role) || seen[role] {
			return errRoleChangeList
		}
		seen[role] = true
	}
	if !seen[change.Role] {
		return errRoleChangeList
	}
	return nil
}

func isAssignableRole(role string) bool {
	return role != domain.GrantRoleService && endUserRoleName.MatchString(role)
}

// roleChangeAudit is what the audit log keeps of one role change: the
// target, the roles asked for, and how it ended (with auth's refusal status
// and code when auth said no).
func roleChangeAudit(userID int64, change endusers.RoleChange, err error) map[string]any {
	details := map[string]any{"userId": userID, "role": change.Role, "allowedRoles": change.AllowedRoles}
	var refused *endusers.RefusedError
	switch {
	case err == nil:
		details["outcome"] = outcomeSuccess
	case errors.As(err, &refused):
		details["outcome"] = outcomeRefused
		details["status"] = refused.Status
		if refused.Code != "" {
			details["code"] = refused.Code
		}
	case errors.Is(err, endusers.ErrAuthUnavailable):
		details["outcome"] = outcomeAuthUnavailable
	default:
		details["outcome"] = outcomeError
	}
	return details
}

func (h *EndUsersHandler) recordRoleChange(r *http.Request, target endUserTarget, details map[string]any) {
	if h.audit == nil {
		return
	}
	encoded, _ := json.Marshal(details)
	now := time.Now()
	entry := &domain.AuditEntry{
		UserID: target.actor, Action: auditActionEndUserRoleSet, Resource: auditResourceEndUser,
		ResourceID: target.projectID, Details: string(encoded), IPAddress: clientIP(r), Timestamp: &now,
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", auditActionEndUserRoleSet, target.projectID, err)
	}
}

// writeEndUserError relays auth's own 4xx verdict unchanged. A 401 is the
// exception: it means auth refused our token, and relayed as-is Studio would
// read it as the admin's session ending.
func writeEndUserError(w http.ResponseWriter, err error) {
	var refused *endusers.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
		log.Printf("ERROR: auth refused the user-admin token: %v", err)
		writeCodedError(w, http.StatusBadGateway, codeAuthRejectedCredential, "the auth service refused the control plane's credential")
	case errors.As(err, &refused):
		body := map[string]any{"error": refused.Message, "status": refused.Status}
		if refused.Code != "" {
			body["code"] = refused.Code
		}
		writeJSONStatus(w, refused.Status, body)
	case errors.Is(err, endusers.ErrAuthUnavailable):
		log.Printf("ERROR: end users: %v", err)
		writeCodedError(w, http.StatusBadGateway, codeAuthUnavailable, "the auth service is unavailable; try again")
	default:
		log.Printf("ERROR: end users: %v", err)
		writeCodedError(w, http.StatusInternalServerError, codeInternalError, "failed to manage end users")
	}
}

func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, map[string]any{"error": message, "code": code, "status": status})
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

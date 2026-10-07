package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/go-chi/chi/v5"
)

const (
	defaultActivityLimit = 100
	maxActivityLimit     = 500
)

// ProjectAuditReader reads one project's audit entries written through one door.
type ProjectAuditReader interface {
	QueryProjectAudit(ctx context.Context, projectID, via string, limit int) ([]domain.AuditEntry, error)
}

// TokenLister lists a user's access tokens.
type TokenLister interface {
	ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error)
}

// ActivityRevokeStore is what revoking a token from the feed reads and writes.
type ActivityRevokeStore interface {
	ProjectAuditUsedToken(ctx context.Context, projectID, via, tokenHash string) (bool, error)
	FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error)
	DeleteToken(ctx context.Context, tokenHash string) error
	GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error)
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// AIActivityHandler lists what AI tools did in a project through MCP (EXC-544).
type AIActivityHandler struct {
	audit   ProjectAuditReader
	tokens  TokenLister
	revokes ActivityRevokeStore
}

func NewAIActivityHandler(audit ProjectAuditReader, tokens TokenLister) *AIActivityHandler {
	return &AIActivityHandler{audit: audit, tokens: tokens}
}

// SetRevokeStore turns on revoking a token from the feed.
func (h *AIActivityHandler) SetRevokeStore(store ActivityRevokeStore) { h.revokes = store }

func (h *AIActivityHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Delete("/tokens/{tokenHash}", h.Revoke)
}

// aiActivityView is one MCP call. TokenID, the id revoke takes, is set while
// the token exists and the caller may revoke it: their own, or any member's
// for an org owner or admin.
type aiActivityView struct {
	ID           int64      `json:"id"`
	Tool         string     `json:"tool"`
	Status       string     `json:"status"`
	HTTPStatus   int        `json:"httpStatus,omitempty"`
	TokenName    string     `json:"tokenName"`
	UserID       string     `json:"userId"`
	At           *time.Time `json:"at"`
	Mine         bool       `json:"mine"`
	TokenID      string     `json:"tokenId,omitempty"`
	TokenRevoked bool       `json:"tokenRevoked,omitempty"`
}

func (h *AIActivityHandler) List(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return
	}
	limit, ok := activityLimit(w, r)
	if !ok {
		return
	}
	entries, err := h.audit.QueryProjectAudit(r.Context(), chi.URLParam(r, "projectId"), domain.AuditViaMCP, limit)
	if err != nil {
		log.Printf("ai activity: read audit: %v", err)
		httpError(w, "could not read the activity", http.StatusInternalServerError)
		return
	}
	revokesAny := h.revokes != nil && orgAdmin(r)
	owners := []string{user.ID}
	if revokesAny {
		owners = entryUsers(entries)
	}
	live, err := h.liveTokens(r.Context(), owners)
	if err != nil {
		log.Printf("ai activity: list tokens of %s: %v", user.ID, err)
		httpError(w, "could not read the activity", http.StatusInternalServerError)
		return
	}
	calls := make([]aiActivityView, 0, len(entries))
	for _, entry := range entries {
		calls = append(calls, activityView(entry, user.ID, revokesAny, live))
	}
	writeJSON(w, map[string]any{"calls": calls})
}

// orgAdmin reports whether the caller is an owner or admin of the project's org.
func orgAdmin(r *http.Request) bool {
	access := middleware.ProjectAccessFromContext(r.Context())
	return access != nil && access.RoleAtLeast(domain.OrgRoleAdmin)
}

func entryUsers(entries []domain.AuditEntry) []string {
	seen := map[string]bool{}
	users := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.UserID != "" && !seen[entry.UserID] {
			seen[entry.UserID] = true
			users = append(users, entry.UserID)
		}
	}
	return users
}

func (h *AIActivityHandler) liveTokens(ctx context.Context, userIDs []string) (map[string]bool, error) {
	live := map[string]bool{}
	for _, userID := range userIDs {
		tokens, err := h.tokens.ListTokensByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, token := range tokens {
			live[token.TokenHash] = true
		}
	}
	return live, nil
}

// Revoke serves DELETE .../ai-activity/tokens/{tokenHash}: a token the
// project's feed shows, revoked by its owner or by an org owner or admin
// when it belongs to a member of the org.
func (h *AIActivityHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return
	}
	if h.revokes == nil {
		httpError(w, "revoking is not available", http.StatusServiceUnavailable)
		return
	}
	token, ok := h.feedToken(w, r)
	if !ok {
		return
	}
	if token.UserID != user.ID && !h.mayRevokeMembersToken(w, r, token) {
		return
	}
	if refused := restrictToCallerToken(auth.GetToken(r.Context()), token.ProjectID, token.Scopes); refused != nil {
		httpError(w, "a token may not revoke a token broader than itself", refused.status)
		return
	}
	if err := h.revokes.DeleteToken(r.Context(), token.TokenHash); err != nil {
		log.Printf("ai activity: revoke %s: %v", token.TokenPrefix, err)
		httpError(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	h.auditRevoke(r, token, user.ID)
	writeJSON(w, map[string]string{"status": "revoked"})
}

// feedToken is the token the path names, provided this project's feed shows it.
func (h *AIActivityHandler) feedToken(w http.ResponseWriter, r *http.Request) (*domain.AccessToken, bool) {
	hash := chi.URLParam(r, "tokenHash")
	used, err := h.revokes.ProjectAuditUsedToken(r.Context(), chi.URLParam(r, "projectId"), domain.AuditViaMCP, hash)
	if err != nil {
		log.Printf("ai activity: look up token use: %v", err)
		httpError(w, "revoke failed", http.StatusInternalServerError)
		return nil, false
	}
	var token *domain.AccessToken
	if used {
		token, err = h.revokes.FindByTokenHash(r.Context(), hash)
	}
	if err != nil {
		log.Printf("ai activity: look up token: %v", err)
		httpError(w, "revoke failed", http.StatusInternalServerError)
		return nil, false
	}
	if token == nil {
		httpError(w, "no such token in this project's AI activity", http.StatusNotFound)
		return nil, false
	}
	return token, true
}

func (h *AIActivityHandler) mayRevokeMembersToken(w http.ResponseWriter, r *http.Request, token *domain.AccessToken) bool {
	const refusal = "only an organization owner or admin may revoke another member's token"
	access := middleware.ProjectAccessFromContext(r.Context())
	if access == nil || !access.RoleAtLeast(domain.OrgRoleAdmin) {
		httpError(w, refusal, http.StatusForbidden)
		return false
	}
	if access.PlatformAdmin {
		return true
	}
	member, err := h.revokes.GetOrgMember(r.Context(), access.Instance.OrgID, token.UserID)
	if err != nil {
		log.Printf("ai activity: look up member: %v", err)
		httpError(w, "revoke failed", http.StatusInternalServerError)
		return false
	}
	if member == nil {
		httpError(w, "the token's owner is not a member of this organization", http.StatusForbidden)
		return false
	}
	return true
}

func (h *AIActivityHandler) auditRevoke(r *http.Request, token *domain.AccessToken, revokedBy string) {
	details, err := json.Marshal(map[string]string{"name": token.Name, "revokedBy": revokedBy, "from": "ai-activity"})
	if err != nil {
		log.Printf("ai activity: encode revoke audit: %v", err)
		return
	}
	now := time.Now()
	entry := &domain.AuditEntry{
		UserID: token.UserID, Action: auditActionTokenRevoke, Resource: auditResourceToken, ResourceID: token.TokenPrefix,
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now, ProjectID: chi.URLParam(r, "projectId"),
	}
	if err := h.revokes.LogAudit(r.Context(), entry); err != nil {
		log.Printf("WARN: audit token revoke %s: %v", token.TokenPrefix, err)
	}
}

func activityView(entry domain.AuditEntry, callerID string, revokesAny bool, live map[string]bool) aiActivityView {
	var details struct {
		Tool       string `json:"tool"`
		Status     string `json:"status"`
		HTTPStatus int    `json:"httpStatus"`
		TokenName  string `json:"tokenName"`
	}
	if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
		log.Printf("ai activity: entry %d has unreadable details: %v", entry.ID, err)
	}
	view := aiActivityView{
		ID: entry.ID, Tool: entry.ResourceID, Status: details.Status, HTTPStatus: details.HTTPStatus,
		TokenName: details.TokenName, UserID: entry.UserID, At: entry.Timestamp, Mine: entry.UserID == callerID,
	}
	if (view.Mine || revokesAny) && entry.TokenHash != "" {
		if live[entry.TokenHash] {
			view.TokenID = entry.TokenHash
		} else {
			view.TokenRevoked = true
		}
	}
	return view
}

func activityLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultActivityLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxActivityLimit {
		httpError(w, "limit must be between 1 and "+strconv.Itoa(maxActivityLimit), http.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

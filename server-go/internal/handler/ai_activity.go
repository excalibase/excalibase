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

// AIActivityHandler lists what AI tools did in a project through MCP (EXC-544).
type AIActivityHandler struct {
	audit  ProjectAuditReader
	tokens TokenLister
}

func NewAIActivityHandler(audit ProjectAuditReader, tokens TokenLister) *AIActivityHandler {
	return &AIActivityHandler{audit: audit, tokens: tokens}
}

func (h *AIActivityHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
}

// aiActivityView is one MCP call. TokenID, the id revoke takes, is set only
// for the caller's own token while it still exists.
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
	live, err := h.liveTokens(r.Context(), user.ID)
	if err != nil {
		log.Printf("ai activity: list tokens of %s: %v", user.ID, err)
		httpError(w, "could not read the activity", http.StatusInternalServerError)
		return
	}
	calls := make([]aiActivityView, 0, len(entries))
	for _, entry := range entries {
		calls = append(calls, activityView(entry, user.ID, live))
	}
	writeJSON(w, map[string]any{"calls": calls})
}

func (h *AIActivityHandler) liveTokens(ctx context.Context, userID string) (map[string]bool, error) {
	tokens, err := h.tokens.ListTokensByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		live[token.TokenHash] = true
	}
	return live, nil
}

func activityView(entry domain.AuditEntry, callerID string, live map[string]bool) aiActivityView {
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
	if view.Mine && live[entry.TokenHash] {
		view.TokenID = entry.TokenHash
	} else if view.Mine {
		view.TokenRevoked = true
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

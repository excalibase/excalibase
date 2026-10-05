package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type fakeProjectAudit struct {
	entries   []domain.AuditEntry
	err       error
	projectID string
	via       string
	limit     int
}

func (f *fakeProjectAudit) QueryProjectAudit(_ context.Context, projectID, via string, limit int) ([]domain.AuditEntry, error) {
	f.projectID, f.via, f.limit = projectID, via, limit
	return f.entries, f.err
}

type fakeTokenList map[string][]*domain.AccessToken

func (f fakeTokenList) ListTokensByUser(_ context.Context, userID string) ([]*domain.AccessToken, error) {
	return f[userID], nil
}

func serveActivity(t *testing.T, h *AIActivityHandler, target, userID string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/ai-activity", h.Routes)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: userID, Active: true}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAIActivityListsCallsAndOffersRevokeOnlyForTheCallersLiveTokens(t *testing.T) {
	at := time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC)
	audit := &fakeProjectAudit{entries: []domain.AuditEntry{
		{ID: 3, UserID: "me", ResourceID: "apply_migration", Details: `{"tool":"apply_migration","status":"error","httpStatus":403,"tokenName":"laptop"}`, TokenHash: "live", Timestamp: &at},
		{ID: 2, UserID: "me", ResourceID: "list_tables", Details: `{"tool":"list_tables","status":"ok","tokenName":"old"}`, TokenHash: "revoked", Timestamp: &at},
		{ID: 1, UserID: "teammate", ResourceID: "list_tables", Details: `{"tool":"list_tables","status":"ok","tokenName":"theirs"}`, TokenHash: "theirs", Timestamp: &at},
	}}
	tokens := fakeTokenList{"me": {{TokenHash: "live"}}, "teammate": {{TokenHash: "theirs"}}}
	w := serveActivity(t, NewAIActivityHandler(audit, tokens), "/api/projects/proj-a/ai-activity/", "me")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if audit.projectID != "proj-a" || audit.via != domain.AuditViaMCP || audit.limit != defaultActivityLimit {
		t.Fatalf("query = %+v", audit)
	}
	var body struct {
		Calls []aiActivityView `json:"calls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Calls) != 3 {
		t.Fatalf("calls = %+v", body.Calls)
	}
	mine, revoked, theirs := body.Calls[0], body.Calls[1], body.Calls[2]
	if mine.Tool != "apply_migration" || mine.Status != "error" || mine.HTTPStatus != 403 || mine.TokenName != "laptop" ||
		mine.TokenID != "live" || !mine.Mine || mine.TokenRevoked {
		t.Errorf("own live token call = %+v", mine)
	}
	if revoked.TokenID != "" || !revoked.TokenRevoked {
		t.Errorf("a revoked token offers no revoke: %+v", revoked)
	}
	if theirs.TokenID != "" || theirs.Mine || theirs.TokenName != "theirs" {
		t.Errorf("someone else's token id must never be shown: %+v", theirs)
	}
}

func TestAIActivityLimit(t *testing.T) {
	audit := &fakeProjectAudit{}
	h := NewAIActivityHandler(audit, fakeTokenList{})
	if w := serveActivity(t, h, "/api/projects/proj-a/ai-activity/?limit=20", "me"); w.Code != http.StatusOK || audit.limit != 20 {
		t.Fatalf("status %d limit %d", w.Code, audit.limit)
	}
	if w := serveActivity(t, h, "/api/projects/proj-a/ai-activity/?limit=5000", "me"); w.Code != http.StatusBadRequest {
		t.Fatalf("an oversize limit must be refused, got %d", w.Code)
	}
}

func TestAIActivityReadFailureIsAnError(t *testing.T) {
	h := NewAIActivityHandler(&fakeProjectAudit{err: errors.New("db down")}, fakeTokenList{})
	w := serveActivity(t, h, "/api/projects/proj-a/ai-activity/", "me")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
}

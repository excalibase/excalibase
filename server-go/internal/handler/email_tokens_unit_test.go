package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestEmailTokens_Rebind(t *testing.T) {
	// SQLite path: placeholders unchanged.
	sqlite := &EmailTokensHandler{pg: false}
	const q = `INSERT INTO t (a, b, c) VALUES (?, ?, ?)`
	if got := sqlite.rebind(q); got != q {
		t.Errorf("sqlite rebind should be no-op, got %q", got)
	}
	// Postgres path: ? → $1, $2, $3.
	pg := &EmailTokensHandler{pg: true}
	want := `INSERT INTO t (a, b, c) VALUES ($1, $2, $3)`
	if got := pg.rebind(q); got != want {
		t.Errorf("pg rebind: got %q, want %q", got, want)
	}
}

func TestNewEmailTokensHandler_DefaultsProductName(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app.example.com/", "")
	if h.productName != "Excalibase" {
		t.Errorf("default product name: got %q", h.productName)
	}
	// Trailing slash on publicBase is trimmed.
	if h.publicBase != "https://app.example.com" {
		t.Errorf("publicBase trim: got %q", h.publicBase)
	}
}

func TestEmailTokens_SendVerify_RequiresAuth(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	req := httptest.NewRequest("POST", "/verify/send", nil)
	w := httptest.NewRecorder()
	h.SendVerify(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no user → 401, got %d", w.Code)
	}
}

func TestEmailTokens_SendVerify_RequiresEmail(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	req := httptest.NewRequest("POST", "/verify/send", nil)
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "u", Email: ""}))
	w := httptest.NewRecorder()
	h.SendVerify(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("no email → 400, got %d", w.Code)
	}
}

func TestEmailTokens_ConfirmVerify_RequiresToken(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	req := httptest.NewRequest("POST", "/verify/confirm", nil)
	w := httptest.NewRecorder()
	h.ConfirmVerify(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing token → 400, got %d", w.Code)
	}
}

func TestEmailTokens_SendReset_RequiresEmail(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	req := httptest.NewRequest("POST", "/reset/send", nil)
	w := httptest.NewRecorder()
	h.SendReset(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing email → 400, got %d", w.Code)
	}
}

func TestEmailTokens_ConfirmReset_RequiresToken(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	req := httptest.NewRequest("POST", "/reset/confirm", nil)
	w := httptest.NewRecorder()
	h.ConfirmReset(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing token/password → 400, got %d", w.Code)
	}
}

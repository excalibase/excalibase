package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
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

// EXC-418: /verify/send used to sit outside RequireAuth, so the token's scopes
// never applied and a read-only credential could make the platform send mail.
func TestEmailTokens_VerifySendMountRequiresAuth(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/email", func(r chi.Router) {
		NewEmailTokensHandler(nil, nil, nil, "https://app", "App").Routes(r)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/email/verify/send", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 from the mount's auth gate", w.Code)
	}

	// The three flows that exist for someone who cannot log in stay public:
	// they answer on their own input, not with 401.
	for _, path := range []string{"/api/email/verify/confirm", "/api/email/reset/send", "/api/email/reset/confirm"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code == http.StatusUnauthorized {
			t.Errorf("%s must stay public", path)
		}
	}
}

// The recipient is the authenticated caller's own address. Nothing in the
// request body selects it, so no caller can aim a send at someone else.
func TestEmailTokens_VerifySendMailsOnlyTheCallersOwnAddress(t *testing.T) {
	h := NewEmailTokensHandler(nil, nil, nil, "https://app", "App")
	body := strings.NewReader(`{"email":"victim@example.com","projectId":"proj-other"}`)
	req := httptest.NewRequest("POST", "/verify/send", body)
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "u", Email: ""}))
	w := httptest.NewRecorder()
	h.SendVerify(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d: the body named an address and the handler used it", w.Code)
	}
}

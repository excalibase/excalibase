package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/go-chi/chi/v5"
)

func setupRegisterRouter(t *testing.T) (chi.Router, *sqlitestore.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlitestore.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	authHandler := NewAuthHandler(store, store)
	authHandler.SetOrgStore(store)

	r := chi.NewRouter()
	r.Post("/api/auth/register", authHandler.Register)
	r.Post("/api/auth/login", authHandler.Login)
	return r, store
}

func TestRegister_Success(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	w := httptest.NewRecorder()
	body := `{"username":"alice","email":"alice@test.com","password":"Alice123!"}`
	req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: got %d, want %d. Body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected token in response")
	}
	if resp["user"] == nil {
		t.Error("expected user in response")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	body := `{"username":"alice","email":"alice@test.com","password":"Alice123!"}`
	req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Second registration with same email
	body2 := `{"username":"alice2","email":"alice@test.com","password":"Alice123!"}`
	req2 := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("duplicate: got %d, want %d", w2.Code, http.StatusConflict)
	}
}

func TestRegister_MissingFields(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	tests := []struct {
		name string
		body string
	}{
		{"no username", `{"email":"a@t.com","password":"pass"}`},
		{"no email", `{"username":"a","password":"pass"}`},
		{"no password", `{"username":"a","email":"a@t.com"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("got %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestRegister_WeakPassword(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	tests := []struct {
		name     string
		password string
	}{
		{"too short", "Ab1"},
		{"no uppercase", "abcdefg1"},
		{"no lowercase", "ABCDEFG1"},
		{"no digit", "Abcdefgh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"username":"test","email":"test@t.com","password":"` + tt.password + `"}`
			req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want %d", tt.name, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	tests := []struct {
		name  string
		email string
	}{
		{"no @", "invalid"},
		{"no domain", "user@"},
		{"no tld", "user@host"},
		{"spaces", "user @host.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"username":"test","email":"` + tt.email + `","password":"Test1234"}`
			req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want %d", tt.name, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestRegister_ResolvesPendingInvites(t *testing.T) {
	r, store := setupRegisterRouter(t)
	ctx := t.Context()

	// Create an org and a pending invite
	store.CreateUser(ctx, &domain.User{
		ID: "owner-1", Username: "owner", Email: "owner@t.com",
		PasswordHash: "hash", Role: "user", Active: true,
	})
	store.CreateOrg(ctx, &domain.Org{ID: "org-1", Name: "TestOrg", Slug: "test-org", Tier: domain.Free, OwnerID: "owner-1"})
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-1", UserID: "owner-1", Role: "owner"})
	store.CreatePendingInvite(ctx, &domain.PendingInvite{OrgID: "org-1", Email: "newguy@test.com", Role: "developer", InvitedBy: "owner-1"})

	// Register with the invited email
	body := `{"username":"newguy","email":"newguy@test.com","password":"Newguy123!"}`
	req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: got %d. Body: %s", w.Code, w.Body.String())
	}

	// Check: user should be auto-added to org
	members, _ := store.ListOrgMembers(ctx, "org-1")
	found := false
	for _, m := range members {
		if m.Email == "newguy@test.com" && m.Role == "developer" {
			found = true
		}
	}
	if !found {
		t.Error("pending invite should have been resolved — newguy not found in org members")
	}

	// Check: pending invite should be deleted
	invites, _ := store.FindPendingInvitesByEmail(ctx, "newguy@test.com")
	if len(invites) != 0 {
		t.Errorf("expected 0 pending invites after registration, got %d", len(invites))
	}
}

func TestRegister_CanLoginAfter(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	// Register
	body := `{"username":"bob","email":"bob@test.com","password":"Bobpass123!"}`
	req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Login
	loginBody := `{"username":"bob","password":"Bobpass123!"}`
	loginReq := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, loginReq)

	if w2.Code != http.StatusOK {
		t.Errorf("login after register: got %d, want %d. Body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}
}

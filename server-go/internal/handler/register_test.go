//go:build integration

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	testRegisterPath = "/api/auth/register"
	testNewGuyEmail  = "newguy@test.com"
)

func setupRegisterRouter(t *testing.T) (chi.Router, *pgstore.Store) {
	t.Helper()
	store := pgtest.New(t)

	authHandler := NewAuthHandler(store, store)
	authHandler.SetOrgStore(store)

	r := chi.NewRouter()
	r.Post(testRegisterPath, authHandler.Register)
	r.Post("/api/auth/login", authHandler.Login)
	return r, store
}

func TestRegister_Success(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"username":"alice","email":"alice@test.com","password":%q}`, testutil.FixturePassword("alice-reg"))
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
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

	alicePwd := testutil.FixturePassword("alice-dup")
	body := fmt.Sprintf(`{"username":"alice","email":"alice@test.com","password":%q}`, alicePwd)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Second registration with same email
	body2 := fmt.Sprintf(`{"username":"alice2","email":"alice@test.com","password":%q}`, alicePwd)
	req2 := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body2))
	req2.Header.Set(sharedContentType, sharedMIMEJSON)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("duplicate: got %d, want %d", w2.Code, http.StatusConflict)
	}
}

func TestRegister_MissingFields(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	missingFieldPwd := testutil.FixturePassword("missing-fields")
	tests := []struct {
		name string
		body string
	}{
		{"no username", fmt.Sprintf(`{"email":"a@t.com","password":%q}`, missingFieldPwd)},
		{"no email", fmt.Sprintf(`{"username":"a","password":%q}`, missingFieldPwd)},
		{"no password", `{"username":"a","email":"a@t.com"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(tt.body))
			req.Header.Set(sharedContentType, sharedMIMEJSON)
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
			req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
			req.Header.Set(sharedContentType, sharedMIMEJSON)
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
			body := fmt.Sprintf(`{"username":"test","email":%q,"password":%q}`, tt.email, testutil.FixturePassword("inv-email"))
			req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
			req.Header.Set(sharedContentType, sharedMIMEJSON)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want %d", tt.name, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestRegister_CanLoginAfter(t *testing.T) {
	r, _ := setupRegisterRouter(t)

	bobPwd := testutil.FixturePassword("bob-login")
	// Register
	body := fmt.Sprintf(`{"username":"bob","email":"bob@test.com","password":%q}`, bobPwd)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Login
	loginBody := fmt.Sprintf(`{"username":"bob","password":%q}`, bobPwd)
	loginReq := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(loginBody))
	loginReq.Header.Set(sharedContentType, sharedMIMEJSON)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, loginReq)

	if w2.Code != http.StatusOK {
		t.Errorf("login after register: got %d, want %d. Body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}
}

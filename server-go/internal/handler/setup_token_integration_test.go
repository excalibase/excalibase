//go:build integration

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// EXC-451: no token on a fresh platform is refused outright.
func TestRegister_FirstAdmin_NoToken_RefusedOnRealStore(t *testing.T) {
	r, _, _ := setupRegisterRouter(t)

	body := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q}`,
		testutil.FixturePassword("founder-no-token"))
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("no setup token: got %d, want %d: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

// EXC-451: a wrong token on a fresh platform is refused.
func TestRegister_FirstAdmin_WrongToken_RefusedOnRealStore(t *testing.T) {
	r, _, _ := setupRegisterRouter(t)

	body := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":"totally-wrong"}`,
		testutil.FixturePassword("founder-wrong-token"))
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong setup token: got %d, want %d: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

// EXC-451: two concurrent first registrations racing the real Postgres store
// with the same valid token must produce exactly one platform_admin. This is
// the actual bug — the row-level DELETE lock in Store.CreateFirstAdmin is
// what has to hold under real concurrency, not just the in-memory mock.
func TestRegister_ConcurrentFirstRegistrations_ExactlyOneAdminOnRealStore(t *testing.T) {
	r, store, setupToken := setupRegisterRouter(t)

	names := []string{"racer-a", "racer-b", "racer-c"}
	var wg sync.WaitGroup
	codes := make([]int, len(names))
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			body := fmt.Sprintf(`{"username":%q,"email":"%s@x.test","password":%q,"setupToken":%q}`,
				name, name, testutil.FixturePassword("race-"+name), setupToken)
			req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
			req.Header.Set(sharedContentType, sharedMIMEJSON)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i, name)
	}
	wg.Wait()

	for _, code := range codes {
		if code != http.StatusCreated && code != http.StatusForbidden {
			t.Errorf("unexpected status %d", code)
		}
	}

	users, err := store.FindAllUsers(context.Background())
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	admins := 0
	for _, u := range users {
		if u.Role == "platform_admin" {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("expected exactly 1 platform_admin after concurrent registration, got %d", admins)
	}
}

// EXC-451: the chart bootstrap Job's operator-supplied SETUP_TOKEN can
// register the first admin end to end — adopted at "startup" (bootstrap
// call), then presented by the register call exactly as the Job does.
func TestRegister_FirstAdmin_PresetSetupToken_EndToEnd(t *testing.T) {
	store := pgtest.New(t)
	ctx := t.Context()

	preset := "0123456789abcdef0123456789abcdef0123456789abcdef" // 50 chars
	if _, err := auth.BootstrapSetupToken(ctx, store, preset); err != nil {
		t.Fatalf("bootstrap with preset token: %v", err)
	}

	authHandler := NewAuthHandler(store, store)
	authHandler.SetOrgStore(store)
	authHandler.SetSetupTokenStore(store)
	r := chi.NewRouter()
	r.Post(testRegisterPath, authHandler.Register)

	body := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":%q}`,
		testutil.FixturePassword("founder-preset"), preset)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register with preset setup token: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	user, err := store.FindUserByEmail(ctx, "founder@example.com")
	if err != nil || user == nil {
		t.Fatalf("user not stored: %v", err)
	}
	if user.Role != "platform_admin" {
		t.Errorf("role: got %q, want platform_admin", user.Role)
	}
}

// EXC-451: startup bootstrap must not generate (or reuse) a setup token once
// a platform admin already exists — restarting an installed platform prints
// nothing.
func TestBootstrapSetupToken_AdminAlreadyExists_NoTokenGenerated(t *testing.T) {
	store := pgtest.New(t)
	ctx := t.Context()

	first, err := auth.BootstrapSetupToken(ctx, store, "")
	if err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if first == "" {
		t.Fatal("expected a token on a fresh store")
	}

	// Consume it to create the admin, mirroring a real first registration.
	admin := &domain.User{
		ID:           auth.GenerateID(),
		Username:     "founder",
		Email:        "founder@restart-test.example.com",
		PasswordHash: testutil.FixturePasswordHash(),
		Role:         "platform_admin",
		Active:       true,
		Kind:         domain.UserKindHuman,
	}
	if err := store.CreateFirstAdmin(ctx, auth.HashToken(first), admin); err != nil {
		t.Fatalf("create first admin: %v", err)
	}

	second, err := auth.BootstrapSetupToken(ctx, store, "")
	if err != nil {
		t.Fatalf("second bootstrap (restart): %v", err)
	}
	if second != "" {
		t.Errorf("restart with an admin present must not generate a token, got %q", second)
	}
}

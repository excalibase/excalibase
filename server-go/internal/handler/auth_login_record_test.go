package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// loginRecordingStore is the user store plus the optional sign-in record.
type loginRecordingStore struct {
	*mockUserStore
	mu     sync.Mutex
	logins map[string]string
}

func (s *loginRecordingStore) RecordUserLogin(_ context.Context, userID, ip string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logins[userID] = ip
	return nil
}

func TestAuthLogin_RecordsWhenAndFromWhereTheAccountSignedIn(t *testing.T) {
	users := &loginRecordingStore{mockUserStore: newMockUserStore(), logins: map[string]string{}}
	username := testutil.FixtureToken("carol")
	password := testutil.FixturePassword("carol-login")
	hash := testutil.FixtureVerifiableHash(password)
	now := time.Now()
	users.users["u9"] = &domain.User{ID: "u9", Username: username, PasswordHash: hash, Active: true, EmailVerifiedAt: &now, CreatedAt: &now}

	handler := NewAuthHandler(users, newMockTokenStore())

	req := httptest.NewRequest(http.MethodPost, routeLogin, strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.20:5555"
	recorder := httptest.NewRecorder()
	handler.Login(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("login: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := users.logins["u9"]; got != "203.0.113.20" {
		t.Fatalf("recorded ip = %q", got)
	}
}

package auth

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// failingLookup answers every lookup with the same error, as a platform
// database that cannot be reached does.
type failingLookup struct {
	tokenErr, userErr error
	token             *domain.AccessToken
}

func (f failingLookup) FindByTokenHash(context.Context, string) (*domain.AccessToken, error) {
	return f.token, f.tokenErr
}

func (f failingLookup) FindUserByID(context.Context, string) (*domain.User, error) {
	return nil, f.userErr
}

func serveWithFailingLookup(lookup TokenLookup) *httptest.ResponseRecorder {
	reached := false
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		rec.Header().Set("X-Reached", "1")
	}
	return rec
}

// A caller whose token is fine must not be told it is bad because the
// database did not answer: 401 sends it to rotate a credential that is valid.
func TestExtractAuth_UnavailableStoreAnswers503Not401(t *testing.T) {
	for name, lookup := range map[string]TokenLookup{
		"token lookup timed out":   failingLookup{tokenErr: context.DeadlineExceeded},
		"token lookup lost conn":   failingLookup{tokenErr: driver.ErrBadConn},
		"user lookup timed out":    failingLookup{token: &domain.AccessToken{UserID: "u"}, userErr: context.DeadlineExceeded},
		"pool exhausted for token": failingLookup{tokenErr: storage.ErrPoolBusy},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveWithFailingLookup(lookup)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Error("503 must say when to retry")
			}
			if rec.Header().Get("X-Reached") != "" {
				t.Error("the request went on unauthenticated")
			}
		})
	}
}

// An unknown token is still an unknown token: the request goes on without a
// user and RequireAuth answers 401.
func TestExtractAuth_UnknownTokenStillContinuesUnauthenticated(t *testing.T) {
	rec := serveWithFailingLookup(failingLookup{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the request to continue", rec.Code)
	}
	if rec2 := serveWithFailingLookup(failingLookup{token: &domain.AccessToken{UserID: "u"}, userErr: errors.New("user not found")}); rec2.Code != http.StatusOK {
		t.Fatalf("a missing user is not an outage; status = %d", rec2.Code)
	}
}

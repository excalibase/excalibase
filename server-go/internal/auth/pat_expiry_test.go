package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const day = 24 * time.Hour

func TestParseExpiresIn(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		want      time.Duration
		wantNever bool
		wantErr   bool
	}{
		{name: "empty defaults to 90d", input: "", want: DefaultPATLifetime},
		{name: "never", input: "never", wantNever: true},
		{name: "never is case-insensitive", input: "NEVER", wantNever: true},
		{name: "days suffix", input: "30d", want: 30 * day},
		{name: "go duration", input: "12h", want: 12 * time.Hour},
		{name: "max 365d accepted", input: "365d", want: 365 * day},
		{name: "over max rejected", input: "366d", wantErr: true},
		{name: "zero rejected", input: "0d", wantErr: true},
		{name: "negative rejected", input: "-1h", wantErr: true},
		{name: "garbage rejected", input: "soon", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifetime, err := ParseExpiresIn(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if lifetime.Never != tc.wantNever {
				t.Errorf("Never: got %v want %v", lifetime.Never, tc.wantNever)
			}
			if !tc.wantNever && lifetime.Duration != tc.want {
				t.Errorf("Duration: got %v want %v", lifetime.Duration, tc.want)
			}
		})
	}
}

func TestPATLifetime_ExpiryFrom(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := (PATLifetime{Never: true}).ExpiryFrom(now); got != nil {
		t.Errorf("never should yield nil expiry, got %v", got)
	}
	got := (PATLifetime{Duration: 2 * day}).ExpiryFrom(now)
	if got == nil || !got.Equal(now.Add(2*day)) {
		t.Errorf("expiry: got %v want %v", got, now.Add(2*day))
	}
}

func TestLifetimeOf_ReusesOriginalWindow(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := created.Add(30 * day)

	got := LifetimeOf(&domain.AccessToken{CreatedAt: &created, ExpiresAt: &expires})
	if got.Never || got.Duration != 30*day {
		t.Errorf("30d window: got %+v", got)
	}
	if got := LifetimeOf(&domain.AccessToken{CreatedAt: &created}); !got.Never {
		t.Errorf("nil expiry must stay never, got %+v", got)
	}
	if got := LifetimeOf(&domain.AccessToken{ExpiresAt: &expires}); got.Never || got.Duration != DefaultPATLifetime {
		t.Errorf("missing createdAt must fall back to default, got %+v", got)
	}
}

func TestTokenExpiredAt(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	if TokenExpiredAt(&domain.AccessToken{}, now) {
		t.Error("nil expiry must never expire")
	}
	if !TokenExpiredAt(&domain.AccessToken{ExpiresAt: &past}, now) {
		t.Error("past expiry must be expired")
	}
	if TokenExpiredAt(&domain.AccessToken{ExpiresAt: &future}, now) {
		t.Error("future expiry must not be expired")
	}
}

func TestRequireAuth_ExpiredTokenGetsDistinctCode(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	rawTok := testutil.FixtureToken("expired-code")
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1", ExpiresAt: &expired},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}
	h := ExtractAuth(lookup)(RequireAuth(okHandler()))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+rawTok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["code"] != ErrCodeTokenExpired {
		t.Errorf("code: got %q want %q", body["code"], ErrCodeTokenExpired)
	}
}

func TestRequireAuth_MissingTokenHasNoExpiredCode(t *testing.T) {
	h := ExtractAuth(&stubLookup{})(RequireAuth(okHandler()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["code"] != "" {
		t.Errorf("unexpected code %q for missing token", body["code"])
	}
}

// touchLookup records TouchTokenLastUsed calls on top of stubLookup.
type touchLookup struct {
	stubLookup
	touched int
}

func (l *touchLookup) TouchTokenLastUsed(_ context.Context, hash string, _ time.Time) error {
	if hash == l.token.TokenHash {
		l.touched++
	}
	return nil
}

func serveWithToken(h http.Handler, raw string) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+raw)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestExtractAuth_TouchesLastUsedWhenStale(t *testing.T) {
	rawTok := testutil.FixtureToken("touch-stale")
	stale := time.Now().Add(-2 * time.Minute)
	lookup := &touchLookup{stubLookup: stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1", LastUsed: &stale},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}}
	serveWithToken(ExtractAuth(lookup)(okHandler()), rawTok)
	if lookup.touched != 1 {
		t.Errorf("stale last_used should be touched once, got %d", lookup.touched)
	}
}

func TestExtractAuth_TouchesLastUsedWhenNeverUsed(t *testing.T) {
	rawTok := testutil.FixtureToken("touch-never")
	lookup := &touchLookup{stubLookup: stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1"},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}}
	serveWithToken(ExtractAuth(lookup)(okHandler()), rawTok)
	if lookup.touched != 1 {
		t.Errorf("never-used token should be touched once, got %d", lookup.touched)
	}
}

func TestExtractAuth_SkipsLastUsedTouchWithinThrottle(t *testing.T) {
	rawTok := testutil.FixtureToken("touch-recent")
	recent := time.Now().Add(-10 * time.Second)
	lookup := &touchLookup{stubLookup: stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1", LastUsed: &recent},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}}
	serveWithToken(ExtractAuth(lookup)(okHandler()), rawTok)
	if lookup.touched != 0 {
		t.Errorf("recently-used token must not be touched, got %d", lookup.touched)
	}
}

func TestExtractAuth_ExpiredTokenNotTouched(t *testing.T) {
	rawTok := testutil.FixtureToken("touch-expired")
	expired := time.Now().Add(-time.Hour)
	lookup := &touchLookup{stubLookup: stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1", ExpiresAt: &expired},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}}
	serveWithToken(ExtractAuth(lookup)(okHandler()), rawTok)
	if lookup.touched != 0 {
		t.Errorf("expired token must not be touched, got %d", lookup.touched)
	}
}

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// stubSetupTokenStore is a minimal in-memory storage.SetupTokenStore for unit
// tests of BootstrapSetupToken — no database needed.
type stubSetupTokenStore struct {
	hasAdmin  bool
	tokenHash string
	storeErr  error
}

func (s *stubSetupTokenStore) HasPlatformAdmin(context.Context) (bool, error) {
	return s.hasAdmin, nil
}

func (s *stubSetupTokenStore) StoreSetupTokenHash(_ context.Context, tokenHash string) error {
	if s.storeErr != nil {
		return s.storeErr
	}
	s.tokenHash = tokenHash
	return nil
}

func (s *stubSetupTokenStore) CreateFirstAdmin(context.Context, string, *domain.User) error {
	return nil
}

func TestBootstrapSetupToken_NoAdmin_GeneratesAndStoresToken(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: false}
	raw, err := BootstrapSetupToken(context.Background(), store, "")
	if err != nil {
		t.Fatalf("BootstrapSetupToken: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a non-empty raw token")
	}
	if store.tokenHash != HashToken(raw) {
		t.Error("stored hash does not match the returned raw token")
	}
}

// EXC-451: restarting an installed platform (an admin already exists) must
// not generate or store a token.
func TestBootstrapSetupToken_AdminExists_NoTokenGenerated(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: true}
	raw, err := BootstrapSetupToken(context.Background(), store, "")
	if err != nil {
		t.Fatalf("BootstrapSetupToken: %v", err)
	}
	if raw != "" {
		t.Errorf("expected no token when an admin exists, got %q", raw)
	}
	if store.tokenHash != "" {
		t.Error("no token hash should have been stored")
	}
}

func TestBootstrapSetupToken_StoreFailure_PropagatesError(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: false, storeErr: errors.New("db down")}
	_, err := BootstrapSetupToken(context.Background(), store, "")
	if err == nil {
		t.Fatal("expected an error when storing the token hash fails")
	}
}

// EXC-451: an operator-supplied SETUP_TOKEN (chart bootstrap Job) is adopted
// as the one-time token instead of generating one, its hash is stored, and
// it is never returned for logging.
func TestBootstrapSetupToken_PresetToken_StoredNotReturned(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: false}
	preset := "0123456789abcdef0123456789abcdef" // 33 chars, >= min
	raw, err := BootstrapSetupToken(context.Background(), store, preset)
	if err != nil {
		t.Fatalf("BootstrapSetupToken: %v", err)
	}
	if raw != "" {
		t.Errorf("a preset token must never be returned for logging, got %q", raw)
	}
	if store.tokenHash != HashToken(preset) {
		t.Error("stored hash does not match the preset token")
	}
}

// A SETUP_TOKEN shorter than MinSetupTokenLength must refuse to start rather
// than silently accept a weak operator-supplied token.
func TestBootstrapSetupToken_PresetTokenTooShort_Errors(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: false}
	_, err := BootstrapSetupToken(context.Background(), store, "too-short")
	if err == nil {
		t.Fatal("expected an error for a SETUP_TOKEN shorter than the minimum length")
	}
	if store.tokenHash != "" {
		t.Error("a rejected preset token must not be stored")
	}
}

// An admin already existing takes priority over a preset token: no store
// write happens even if SETUP_TOKEN is still set in the environment (e.g. a
// restart after install, with the chart's Secret env unchanged).
func TestBootstrapSetupToken_PresetToken_AdminAlreadyExists_Ignored(t *testing.T) {
	store := &stubSetupTokenStore{hasAdmin: true}
	preset := "0123456789abcdef0123456789abcdef"
	raw, err := BootstrapSetupToken(context.Background(), store, preset)
	if err != nil {
		t.Fatalf("BootstrapSetupToken: %v", err)
	}
	if raw != "" {
		t.Errorf("expected no token when an admin exists, got %q", raw)
	}
	if store.tokenHash != "" {
		t.Error("no token hash should have been stored once an admin exists")
	}
}

// GenerateSetupToken produces a URL-safe token of at least 32 random bytes
// (43 base64url characters with no padding), distinct on every call.
func TestGenerateSetupToken_LengthAndUniqueness(t *testing.T) {
	a := GenerateSetupToken()
	b := GenerateSetupToken()
	if len(a) < 32 {
		t.Errorf("token too short: %d chars", len(a))
	}
	if a == b {
		t.Error("two generated tokens must not collide")
	}
	for _, r := range a {
		if r == '+' || r == '/' || r == '=' {
			t.Fatalf("token is not URL-safe: contains %q", r)
		}
	}
}

var _ storage.SetupTokenStore = (*stubSetupTokenStore)(nil)

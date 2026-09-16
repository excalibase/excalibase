package natsauth

import (
	"context"
	"errors"
	"testing"
)

type fakeCredentialStore struct {
	hashes map[string]string
	err    error
}

func (f *fakeCredentialStore) LookupNatsCredentialHash(_ context.Context, principal string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	hash, ok := f.hashes[principal]
	return hash, ok, nil
}

func newStoreWith(t *testing.T, principal, password string) *fakeCredentialStore {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return &fakeCredentialStore{hashes: map[string]string{principal: hash}}
}

func TestNewPasswordIsRandomAndLongEnough(t *testing.T) {
	first, err := NewPassword()
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	second, _ := NewPassword()
	if first == second {
		t.Error("NewPassword returned the same value twice")
	}
	if len(first) < 32 {
		t.Errorf("password length = %d, want >= 32", len(first))
	}
}

func TestHashPasswordDoesNotStorePlaintext(t *testing.T) {
	hash, err := HashPassword("s3cret-value")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-value" {
		t.Error("hash equals plaintext")
	}
}

func TestAuthenticateAcceptsCorrectPassword(t *testing.T) {
	store := newStoreWith(t, PrincipalGraphQL, "correct-horse")
	if err := Authenticate(context.Background(), store, PrincipalGraphQL, "correct-horse"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}

func TestAuthenticateFailsClosed(t *testing.T) {
	good := newStoreWith(t, PrincipalGraphQL, "correct-horse")
	broken := &fakeCredentialStore{err: errors.New("db down")}

	cases := []struct {
		name      string
		store     CredentialStore
		principal string
		password  string
	}{
		{"wrong password", good, PrincipalGraphQL, "wrong"},
		{"empty password", good, PrincipalGraphQL, ""},
		{"unknown principal", good, "svc-attacker", "correct-horse"},
		{"store unreachable", broken, PrincipalGraphQL, "correct-horse"},
		{"nil store", nil, PrincipalGraphQL, "correct-horse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Authenticate(context.Background(), tc.store, tc.principal, tc.password); err == nil {
				t.Error("Authenticate succeeded, want rejection")
			}
		})
	}
}

func TestAuthenticateDoesNotLeakWhichHalfFailed(t *testing.T) {
	store := newStoreWith(t, PrincipalGraphQL, "correct-horse")
	unknown := Authenticate(context.Background(), store, "svc-nope", "correct-horse")
	wrongPass := Authenticate(context.Background(), store, PrincipalGraphQL, "nope")
	if unknown == nil || wrongPass == nil {
		t.Fatal("expected both to fail")
	}
	if unknown.Error() != wrongPass.Error() {
		t.Errorf("distinguishable errors: %q vs %q", unknown, wrongPass)
	}
}

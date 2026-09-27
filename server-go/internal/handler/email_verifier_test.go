package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type fakeVerificationStore struct {
	mu       sync.Mutex
	links    map[string]fakeVerificationLink
	verified map[string]time.Time
	users    map[string]string
	failNext error
}

type fakeVerificationLink struct {
	userID, email string
	expires       time.Time
	used          bool
}

func newFakeVerificationStore() *fakeVerificationStore {
	return &fakeVerificationStore{
		links: map[string]fakeVerificationLink{}, verified: map[string]time.Time{}, users: map[string]string{},
	}
}

func (f *fakeVerificationStore) CreateEmailVerification(_ context.Context, userID, address, hash string, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.links[hash] = fakeVerificationLink{userID: userID, email: address, expires: expires}
	return nil
}

func (f *fakeVerificationStore) ConsumeEmailVerification(_ context.Context, hash string, now time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	link, ok := f.links[hash]
	if !ok || link.used || !now.Before(link.expires) || f.users[link.userID] != link.email {
		return "", storage.ErrEmailVerificationInvalid
	}
	link.used = true
	f.links[hash] = link
	f.verified[link.userID] = now
	return link.userID, nil
}

func (f *fakeVerificationStore) MarkEmailVerified(_ context.Context, userID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return storage.ErrUserNotFound
	}
	f.verified[userID] = now
	return nil
}

type recordingSender struct {
	mu   sync.Mutex
	sent []email.Message
	err  error
}

func (s *recordingSender) Send(_ context.Context, msg email.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.sent = append(s.sent, msg)
	return "msg-1", nil
}

func (s *recordingSender) lastToken(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("no email was sent")
	}
	token := tokenFromMessage(s.sent[len(s.sent)-1].TextBody)
	if token == "" {
		t.Fatalf("no token in %q", s.sent[len(s.sent)-1].TextBody)
	}
	return token
}

func tokenFromMessage(body string) string {
	_, rest, found := strings.Cut(body, "token=")
	if !found {
		return ""
	}
	end := strings.IndexAny(rest, " \n\"'<&")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

const testStudioURL = "https://studio.example.com"

func newTestVerifier() (*EmailVerifier, *fakeVerificationStore, *recordingSender) {
	store := newFakeVerificationStore()
	sender := &recordingSender{}
	return NewEmailVerifier(store, sender, testStudioURL, "Excalibase"), store, sender
}

func TestVerifierMailsAStudioLinkToTheAccountsAddress(t *testing.T) {
	verifier, _, sender := newTestVerifier()
	user := &domain.User{ID: "u1", Email: "dev@example.com"}

	if err := verifier.Send(context.Background(), user); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msg := sender.sent[0]
	if len(msg.To) != 1 || msg.To[0] != "dev@example.com" {
		t.Fatalf("sent to %v", msg.To)
	}
	if !strings.Contains(msg.TextBody, testStudioURL+"/verify-email?token=") {
		t.Fatalf("link is not a Studio link: %q", msg.TextBody)
	}
}

func TestVerifierConfirmsALinkOnce(t *testing.T) {
	verifier, store, sender := newTestVerifier()
	store.users["u1"] = "dev@example.com"
	if err := verifier.Send(context.Background(), &domain.User{ID: "u1", Email: "dev@example.com"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := sender.lastToken(t)

	userID, err := verifier.Confirm(context.Background(), token)
	if err != nil || userID != "u1" {
		t.Fatalf("Confirm: %q %v", userID, err)
	}
	if _, err := verifier.Confirm(context.Background(), token); !errors.Is(err, storage.ErrEmailVerificationInvalid) {
		t.Fatalf("second confirm: %v", err)
	}
}

func TestVerifierLinkExpiresAfterADay(t *testing.T) {
	verifier, store, sender := newTestVerifier()
	store.users["u1"] = "dev@example.com"
	issued := time.Now()
	verifier.now = func() time.Time { return issued }
	if err := verifier.Send(context.Background(), &domain.User{ID: "u1", Email: "dev@example.com"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	verifier.now = func() time.Time { return issued.Add(verificationTTL + time.Second) }

	if _, err := verifier.Confirm(context.Background(), sender.lastToken(t)); !errors.Is(err, storage.ErrEmailVerificationInvalid) {
		t.Fatalf("expired link: %v", err)
	}
}

func TestVerifierStoresOnlyTheHash(t *testing.T) {
	verifier, store, sender := newTestVerifier()
	if err := verifier.Send(context.Background(), &domain.User{ID: "u1", Email: "dev@example.com"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := sender.lastToken(t)
	if _, raw := store.links[token]; raw {
		t.Fatal("the raw token was stored")
	}
	if _, hashed := store.links[hashToken(token)]; !hashed {
		t.Fatal("the token's hash was not stored")
	}
}

func TestVerifierUnavailableWithoutAMailer(t *testing.T) {
	verifier := NewEmailVerifier(newFakeVerificationStore(), email.NewNoopSender(), testStudioURL, "")
	if verifier.Available() {
		t.Fatal("available with the noop sender")
	}
	err := verifier.Send(context.Background(), &domain.User{ID: "u1", Email: "dev@example.com"})
	if !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("Send: %v", err)
	}
	var nilVerifier *EmailVerifier
	if nilVerifier.Available() {
		t.Fatal("a nil verifier is available")
	}
}

func TestVerifierReportsSendAndStoreFailures(t *testing.T) {
	verifier, store, sender := newTestVerifier()
	user := &domain.User{ID: "u1", Email: "dev@example.com"}
	store.failNext = errors.New("db down")
	if err := verifier.Send(context.Background(), user); err == nil {
		t.Fatal("a store failure was swallowed")
	}
	sender.err = errors.New("provider down")
	if err := verifier.Send(context.Background(), user); err == nil {
		t.Fatal("a send failure was swallowed")
	}
}

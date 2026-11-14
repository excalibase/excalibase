package handler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// verificationTTL is how long a Studio verification link stays usable.
const verificationTTL = 24 * time.Hour

// ErrVerificationUnavailable means the platform cannot send verification
// mail, so no account can prove its address.
var ErrVerificationUnavailable = errors.New("email verification is not available")

// EmailVerifier mails and checks the links that prove a Studio account owns
// its email address.
type EmailVerifier struct {
	store       storage.EmailVerificationStore
	sender      email.Sender
	studioURL   string
	productName string
	now         func() time.Time

	noticeMu    sync.Mutex
	noticeSent  map[string]time.Time
	noticeSwept time.Time
}

// noticeInterval is how often one address is told it already has an account, so
// the sign-up form cannot be used to flood a mailbox. Kept in memory: one replica.
const noticeInterval = time.Hour

func NewEmailVerifier(store storage.EmailVerificationStore, sender email.Sender, studioURL, productName string) *EmailVerifier {
	if productName == "" {
		productName = "Excalibase"
	}
	return &EmailVerifier{
		store: store, sender: sender, studioURL: strings.TrimRight(studioURL, "/"),
		productName: productName, now: time.Now, noticeSent: map[string]time.Time{},
	}
}

// Available reports whether verification mail can be sent at all.
func (v *EmailVerifier) Available() bool {
	return v != nil && v.store != nil && v.studioURL != "" && email.Configured(v.sender)
}

// Send files a fresh link for the account's current address and mails it.
func (v *EmailVerifier) Send(ctx context.Context, user *domain.User) error {
	if !v.Available() {
		return ErrVerificationUnavailable
	}
	token, hash, err := mintToken()
	if err != nil {
		return fmt.Errorf("mint verification token: %w", err)
	}
	if err := v.store.CreateEmailVerification(ctx, user.ID, user.Email, hash, v.now().Add(verificationTTL)); err != nil {
		return fmt.Errorf("store verification token: %w", err)
	}
	msg, err := email.BuildVerifyEmail(email.VerifyEmailData{
		UserEmail:   user.Email,
		VerifyURL:   v.studioURL + "/verify-email?" + url.Values{"token": {token}}.Encode(),
		ExpiresHour: int(verificationTTL / time.Hour),
		ProductName: v.productName,
	})
	if err != nil {
		return fmt.Errorf("build verification email: %w", err)
	}
	msg.To = []string{user.Email}
	messageID, err := v.sender.Send(ctx, msg)
	if err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	logEmailSent("verify", user.Email, messageID, user.ID)
	return nil
}

// NotifyAccountExists mails the address a sign-up named, saying why no account
// was made: it already has one, or the chosen username is taken.
func (v *EmailVerifier) NotifyAccountExists(ctx context.Context, address string, usernameTaken bool) error {
	if !v.Available() {
		return ErrVerificationUnavailable
	}
	key := strings.ToLower(strings.TrimSpace(address))
	if !v.reserveNotice(key) {
		return nil
	}
	sent := false
	defer func() {
		if !sent {
			v.releaseNotice(key)
		}
	}()
	msg, err := email.BuildAccountExistsEmail(email.AccountExistsData{
		UserEmail: address, UsernameTaken: usernameTaken, SignInURL: v.studioURL + "/login", ProductName: v.productName,
	})
	if err != nil {
		return fmt.Errorf("build sign-up notice: %w", err)
	}
	msg.To = []string{address}
	messageID, err := v.sender.Send(ctx, msg)
	if err != nil {
		return fmt.Errorf("send sign-up notice: %w", err)
	}
	sent = true
	logEmailSent("account_exists", address, messageID, "")
	return nil
}

// reserveNotice claims the address's hour before the send, so requests in
// parallel cannot all pass the check; it drops the records that have aged out.
func (v *EmailVerifier) reserveNotice(key string) bool {
	v.noticeMu.Lock()
	defer v.noticeMu.Unlock()
	now := v.now()
	if now.Sub(v.noticeSwept) >= noticeInterval {
		v.noticeSwept = now
		for address, sentAt := range v.noticeSent {
			if now.Sub(sentAt) >= noticeInterval {
				delete(v.noticeSent, address)
			}
		}
	}
	if sentAt, recent := v.noticeSent[key]; recent && now.Sub(sentAt) < noticeInterval {
		return false
	}
	v.noticeSent[key] = now
	return true
}

// releaseNotice gives the hour back when the send failed.
func (v *EmailVerifier) releaseNotice(key string) {
	v.noticeMu.Lock()
	defer v.noticeMu.Unlock()
	delete(v.noticeSent, key)
}

// Confirm spends a link and marks its account verified, returning the
// account's id.
func (v *EmailVerifier) Confirm(ctx context.Context, token string) (string, error) {
	if v == nil || v.store == nil {
		return "", ErrVerificationUnavailable
	}
	return v.store.ConsumeEmailVerification(ctx, hashToken(token), v.now())
}

// MarkVerified records that the account's address was proven another way.
func (v *EmailVerifier) MarkVerified(ctx context.Context, userID string) error {
	if v == nil || v.store == nil {
		return ErrVerificationUnavailable
	}
	return v.store.MarkEmailVerified(ctx, userID, v.now())
}

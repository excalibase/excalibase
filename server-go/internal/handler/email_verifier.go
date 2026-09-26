package handler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
}

func NewEmailVerifier(store storage.EmailVerificationStore, sender email.Sender, studioURL, productName string) *EmailVerifier {
	if productName == "" {
		productName = "Excalibase"
	}
	return &EmailVerifier{
		store: store, sender: sender, studioURL: strings.TrimRight(studioURL, "/"),
		productName: productName, now: time.Now,
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

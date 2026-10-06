package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// PersonalOrgStore creates an account's personal organization (EXC-553).
type PersonalOrgStore interface {
	EnsurePersonalOrg(ctx context.Context, org *domain.Org) (bool, error)
}

// personalOrgSlugAttempts bounds the retries when a derived slug is taken:
// the plain username first, then random suffixes.
const personalOrgSlugAttempts = 4

const (
	personalOrgNameSuffix = "'s organization"
	slugBaseMaxLength     = 40
)

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// EnsurePersonalOrg gives a Studio account its free personal organization
// unless it already created an org. Idempotent: safe to call on every
// sign-in. Service principals get none.
func EnsurePersonalOrg(ctx context.Context, store PersonalOrgStore, user *domain.User) error {
	if user.IsService() {
		return nil
	}
	for attempt := range personalOrgSlugAttempts {
		_, err := store.EnsurePersonalOrg(ctx, &domain.Org{
			ID:      GenerateID(),
			Name:    PersonalOrgName(user.Username),
			Slug:    personalOrgSlug(user.Username, attempt),
			Tier:    domain.Free,
			OwnerID: user.ID,
		})
		if !errors.Is(err, storage.ErrOrgSlugTaken) {
			return err
		}
	}
	return fmt.Errorf("personal organization for %s: %w", user.ID, storage.ErrOrgSlugTaken)
}

// PersonalOrgName names the org after the account, within the org name rules.
func PersonalOrgName(username string) string {
	base := strings.TrimSpace(username)
	if base == "" {
		base = "My"
	}
	room := domain.MaxOrgNameLength - utf8.RuneCountInString(personalOrgNameSuffix)
	if runes := []rune(base); len(runes) > room {
		base = strings.TrimSpace(string(runes[:room]))
	}
	return base + personalOrgNameSuffix
}

// personalOrgSlug derives a slug from the username; attempts after the first
// add a random suffix.
func personalOrgSlug(username string, attempt int) string {
	base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(username), "-"), "-")
	if len(base) > slugBaseMaxLength {
		base = strings.TrimRight(base[:slugBaseMaxLength], "-")
	}
	if len(base) < 2 {
		base = "org"
	}
	if attempt == 0 {
		return base
	}
	return base + "-" + randomSlugSuffix()
}

func randomSlugSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}

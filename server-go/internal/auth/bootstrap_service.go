package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BootstrapServiceName is the service principal the chart's bootstrap Job and
// token-rotation CronJob authenticate as (EXC-485). It replaces the platform
// admin password those jobs used to read from a Secret: its capability token
// reaches only the calls they make.
const BootstrapServiceName = "svc-bootstrap"

const (
	bootstrapServiceRole  = "platform_admin"
	bootstrapServiceEmail = BootstrapServiceName + "@svc.excalibase.internal"
)

// AdoptBootstrapServiceToken makes raw the one token svc-bootstrap holds,
// carrying exactly permissions. The value comes from a Secret the chart
// generates, so the Secret is the source of truth: re-running with the same
// value is a no-op apart from following a changed permission list, and a new
// value retires every token the principal held before. That is also how the
// token is revoked — replace the Secret and restart provisioning.
//
// raw == "" means the deployment does not use a bootstrap principal and
// nothing is done.
func AdoptBootstrapServiceToken(ctx context.Context, users storage.UserStore, tokens storage.TokenStore, raw string, permissions []string) error {
	if raw == "" {
		return nil
	}
	if len(raw) < MinSetupTokenLength {
		return fmt.Errorf("the bootstrap service token must be at least %d characters, got %d", MinSetupTokenLength, len(raw))
	}
	normalized, err := NormalizeCapabilities(permissions)
	if err != nil {
		return fmt.Errorf("bootstrap service permissions: %w", err)
	}
	if len(normalized) == 0 {
		return errors.New("the bootstrap service token needs at least one permission")
	}
	principal, err := ensureBootstrapPrincipal(ctx, users)
	if err != nil {
		return err
	}
	return adoptToken(ctx, tokens, principal.ID, HashToken(raw), TokenPrefix(raw), normalized)
}

func ensureBootstrapPrincipal(ctx context.Context, users storage.UserStore) (*domain.User, error) {
	existing, err := users.FindUserByUsername(ctx, BootstrapServiceName)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		return nil, fmt.Errorf("look up %s: %w", BootstrapServiceName, err)
	}
	if existing != nil {
		if !existing.IsService() {
			return nil, fmt.Errorf("the name %s is taken by a user account", BootstrapServiceName)
		}
		return existing, nil
	}
	now := time.Now()
	principal := &domain.User{
		ID:        GenerateID(),
		Username:  BootstrapServiceName,
		Email:     bootstrapServiceEmail,
		Role:      bootstrapServiceRole,
		Active:    true,
		Kind:      domain.UserKindService,
		CreatedAt: &now,
	}
	if err := users.CreateUser(ctx, principal); err != nil {
		return nil, fmt.Errorf("create %s: %w", BootstrapServiceName, err)
	}
	return principal, nil
}

// adoptToken leaves the principal holding exactly one token: hash, with
// permissions. A token row whose permissions drifted is re-created under the
// same hash so the Secret's value keeps working.
func adoptToken(ctx context.Context, tokens storage.TokenStore, userID, hash, prefix string, permissions []string) error {
	held, err := tokens.ListTokensByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("list %s tokens: %w", BootstrapServiceName, err)
	}
	current := false
	for _, token := range held {
		if token.TokenHash == hash && slices.Equal(token.Permissions, permissions) {
			current = true
			continue
		}
		if err := tokens.DeleteToken(ctx, token.TokenHash); err != nil {
			return fmt.Errorf("retire %s token: %w", BootstrapServiceName, err)
		}
	}
	if current {
		return nil
	}
	now := time.Now()
	return tokens.CreateToken(ctx, &domain.AccessToken{
		TokenHash:   hash,
		TokenPrefix: prefix,
		UserID:      userID,
		Name:        BootstrapServiceName,
		Permissions: permissions,
		CreatedAt:   &now,
	})
}

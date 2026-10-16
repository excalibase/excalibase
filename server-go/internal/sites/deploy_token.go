package sites

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/google/uuid"
)

// A deploy token (EXC-620) lets CI deploy one site and read that site's
// deploys, nothing else. It acts with the authority of the Developer who made
// it, narrowed to that site, and is kept only as its SHA-256, as a personal
// access token is.
const (
	// DeployTokenPrefix tells a deploy token from a personal access token at a glance.
	DeployTokenPrefix = "excd_"
	// MaxDeployTokensPerSite bounds the tokens one site holds.
	MaxDeployTokensPerSite = 10
	maxTokenNameLength     = 64
	// tokenTouchInterval throttles last-used writes to one a minute per token.
	tokenTouchInterval = time.Minute
	// DefaultDeployTokenLifetime and MaxDeployTokenLifetime bound how long a token lives.
	DefaultDeployTokenLifetime = 90 * 24 * time.Hour
	MaxDeployTokenLifetime     = 365 * 24 * time.Hour
)

var (
	ErrDeployTokenNotFound     = errors.New("deploy token not found")
	ErrDeployTokenLimitReached = errors.New("the site already holds as many deploy tokens as it may; revoke one first")
)

// DeployToken is one site's CI credential; Hash is the SHA-256 of the raw token.
type DeployToken struct {
	ID         string     `json:"id"`
	SiteID     string     `json:"siteId"`
	ProjectID  string     `json:"projectId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"tokenPrefix"`
	Hash       string     `json:"-"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// DeployTokenRequest is one deploy token to make.
type DeployTokenRequest struct {
	ProjectID string
	SiteID    string
	Name      string
	Actor     string
	// Lifetime is how long it lives; 0 is DefaultDeployTokenLifetime.
	Lifetime time.Duration
	// NotAfter caps the expiry at the credential that made it, so it never outlives it.
	NotAfter *time.Time
}

// expiry answers when the requested token expires, refusing a lifetime past a year.
func (request DeployTokenRequest) expiry(now time.Time) (time.Time, error) {
	lifetime := request.Lifetime
	if lifetime == 0 {
		lifetime = DefaultDeployTokenLifetime
	}
	if lifetime < 0 || lifetime > MaxDeployTokenLifetime {
		return time.Time{}, &InputError{Reason: "a deploy token lives at most 365 days"}
	}
	expires := now.Add(lifetime)
	if request.NotAfter != nil && request.NotAfter.Before(expires) {
		expires = request.NotAfter.UTC()
	}
	if !expires.After(now) {
		return time.Time{}, &InputError{Reason: "the credential making this deploy token has expired"}
	}
	return expires, nil
}

// IsDeployToken reports whether raw is shaped as a deploy token.
func IsDeployToken(raw string) bool { return strings.HasPrefix(raw, DeployTokenPrefix) }

func generateDeployToken() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate deploy token: %w", err)
	}
	return DeployTokenPrefix + hex.EncodeToString(secret), nil
}

func validateTokenName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(name) > maxTokenNameLength {
		return fmt.Errorf("a deploy token name is 1 to %d characters", maxTokenNameLength)
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return errors.New("a deploy token name may not contain control or formatting characters")
	}
	return nil
}

// CreateDeployToken answers the raw token, which is never stored or shown again.
func (s *Service) CreateDeployToken(ctx context.Context, request DeployTokenRequest) (string, *DeployToken, error) {
	if !s.Available() {
		return "", nil, ErrSitesUnavailable
	}
	if err := validateTokenName(request.Name); err != nil {
		return "", nil, &InputError{Reason: err.Error()}
	}
	now := s.now().UTC()
	expires, err := request.expiry(now)
	if err != nil {
		return "", nil, err
	}
	if _, err := s.mustGet(ctx, request.ProjectID, request.SiteID); err != nil {
		return "", nil, err
	}
	raw, err := generateDeployToken()
	if err != nil {
		return "", nil, err
	}
	token := &DeployToken{
		ID: uuid.NewString(), SiteID: request.SiteID, ProjectID: request.ProjectID, Name: strings.TrimSpace(request.Name),
		Prefix: auth.TokenPrefix(raw), Hash: auth.HashToken(raw), CreatedBy: request.Actor, CreatedAt: now, ExpiresAt: expires,
	}
	if err := s.store.CreateDeployToken(ctx, token, MaxDeployTokensPerSite); err != nil {
		return "", nil, err
	}
	return raw, token, nil
}

func (s *Service) ListDeployTokens(ctx context.Context, projectID, siteID string) ([]*DeployToken, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if _, err := s.mustGet(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.store.ListDeployTokens(ctx, projectID, siteID)
}

// RevokeDeployToken deletes the token; the next request carrying it is refused.
func (s *Service) RevokeDeployToken(ctx context.Context, projectID, siteID, id string) error {
	if !s.Available() {
		return ErrSitesUnavailable
	}
	return s.store.DeleteDeployToken(ctx, projectID, siteID, id)
}

// ResolveDeployToken answers the live token raw names, or nil when it names none.
func (s *Service) ResolveDeployToken(ctx context.Context, raw string) (*DeployToken, error) {
	if s == nil || !IsDeployToken(raw) {
		return nil, nil
	}
	token, err := s.store.FindDeployToken(ctx, auth.HashToken(raw))
	if err != nil || token == nil {
		return nil, err
	}
	if !s.now().Before(token.ExpiresAt) {
		return nil, nil
	}
	return token, nil
}

// RecordDeployTokenUse marks the token used, at most once a minute; best
// effort, so a failed write never refuses the deploy.
func (s *Service) RecordDeployTokenUse(ctx context.Context, token *DeployToken) {
	now := s.now().UTC()
	if token.LastUsedAt != nil && now.Sub(*token.LastUsedAt) < tokenTouchInterval {
		return
	}
	if err := s.store.TouchDeployToken(ctx, token.ID, now); err != nil {
		log.Printf("site %s: record deploy token use: %v", token.SiteID, err)
	}
}

type deployTokenKey struct{}

// WithDeployToken marks the request as made with token, so its deploy records it.
func WithDeployToken(ctx context.Context, token *DeployToken) context.Context {
	return context.WithValue(ctx, deployTokenKey{}, token)
}

// DeployTokenFrom is the deploy token the request was made with, or nil.
func DeployTokenFrom(ctx context.Context) *DeployToken {
	token, _ := ctx.Value(deployTokenKey{}).(*DeployToken)
	return token
}

package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	// DefaultPATLifetime applies when a caller creates a PAT without expiresIn.
	DefaultPATLifetime = 90 * 24 * time.Hour
	// MaxPATLifetime caps expiresIn; only the explicit "never" escapes it.
	MaxPATLifetime = 365 * 24 * time.Hour
	// ExpiresNever is the expiresIn literal for a non-expiring PAT.
	ExpiresNever = "never"
	// ErrCodeTokenExpired is the machine-readable 401 code for an expired token.
	ErrCodeTokenExpired = "token_expired"
	// lastUsedThrottle bounds last_used writes to one per token per interval.
	lastUsedThrottle = time.Minute
)

// ErrInvalidExpiresIn is returned for an unparsable or out-of-range expiresIn.
var ErrInvalidExpiresIn = errors.New("expiresIn must be a duration like 30d or 12h (max 365d) or \"never\"")

// PATLifetime is a parsed expiresIn: either Never or a positive Duration.
type PATLifetime struct {
	Duration time.Duration
	Never    bool
}

// ExpiryFrom resolves the lifetime to an absolute expiry starting at now.
// Never yields nil, which the store persists as NULL (= no expiry).
func (l PATLifetime) ExpiryFrom(now time.Time) *time.Time {
	if l.Never {
		return nil
	}
	at := now.Add(l.Duration)
	return &at
}

// ParseExpiresIn parses the expiresIn request field. Empty selects the
// default; "never" is the only way to mint a non-expiring PAT; anything
// else is a Go duration or a "<n>d" day count, bounded by MaxPATLifetime.
func ParseExpiresIn(raw string) (PATLifetime, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "":
		return PATLifetime{Duration: DefaultPATLifetime}, nil
	case ExpiresNever:
		return PATLifetime{Never: true}, nil
	}
	d, err := parseDaysOrDuration(raw)
	if err != nil || d <= 0 || d > MaxPATLifetime {
		return PATLifetime{}, ErrInvalidExpiresIn
	}
	return PATLifetime{Duration: d}, nil
}

func parseDaysOrDuration(raw string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("parse days: %w", err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

// LifetimeOf recovers the lifetime a token was minted with, so a rotation
// can re-issue it "from now". A NULL expiry stays never; a token whose
// creation time is unknown falls back to the default.
func LifetimeOf(t *domain.AccessToken) PATLifetime {
	if t.ExpiresAt == nil {
		return PATLifetime{Never: true}
	}
	if t.CreatedAt == nil {
		return PATLifetime{Duration: DefaultPATLifetime}
	}
	return PATLifetime{Duration: t.ExpiresAt.Sub(*t.CreatedAt)}
}

// TokenExpiredAt reports whether t is expired as of now. NULL ExpiresAt =
// never expires (long-lived CI token).
func TokenExpiredAt(t *domain.AccessToken, now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// LastUsedRecorder is implemented by token lookups that can persist
// last_used. Optional: ExtractAuth only records when the lookup offers it.
type LastUsedRecorder interface {
	TouchTokenLastUsed(ctx context.Context, tokenHash string, at time.Time) error
}

// lastUsedStale reports whether the token's last_used is old enough to be
// written again; the read that authenticated the request already carries
// the previous value, so no in-memory throttle state is needed.
func lastUsedStale(t *domain.AccessToken, now time.Time) bool {
	return t.LastUsed == nil || now.Sub(*t.LastUsed) >= lastUsedThrottle
}

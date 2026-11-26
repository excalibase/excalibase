package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// TrustConfig is every number behind the limited trial (EXC-899). Nothing in
// the code decides a threshold; the values below are the defaults the owner
// chose on 2026-11-26. A mistyped value stops the boot rather than falling
// back, since a silent default would quietly change who is limited.
type TrustConfig struct {
	// GitHubMinAgeDays is how old the linked GitHub account must be.
	GitHubMinAgeDays int
	// GitHubMinPublicEvents within GitHubActivityWindowDays counts as activity...
	GitHubMinPublicEvents    int
	GitHubActivityWindowDays int
	// ...as do GitHubMinRepos public repositories, one older than GitHubRepoMinAgeDays.
	GitHubMinRepos       int
	GitHubRepoMinAgeDays int
	// GitHubRecheckDays re-checks a verified account that often; 0 verifies once.
	GitHubRecheckDays int
	// VerifyRetryMinutes is the wait between two verify attempts of one account.
	VerifyRetryMinutes int
	// GrandfatherBefore: accounts created before this are made full by the backfill.
	GrandfatherBefore time.Time
	// ClaimCooldownDays is how long a released GitHub claim stays unusable by another account.
	ClaimCooldownDays int
	// EdgeLimitedMbps is the outbound rate the edge allows a limited account's hosts.
	EdgeLimitedMbps int
	// EdgeLimitedAllowWS lets a limited account's hosts upgrade to WebSocket.
	EdgeLimitedAllowWS bool
	// PodLimitedEgressMbps is the cap on a limited account's app pods' own outbound traffic.
	PodLimitedEgressMbps int

	err error
}

func loadTrust() TrustConfig {
	var errs []string
	number := func(key string, fallback, minimum int) int {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < minimum {
			errs = append(errs, fmt.Sprintf("%s=%q is not a whole number of at least %d", key, raw, minimum))
			return fallback
		}
		return n
	}
	cfg := TrustConfig{
		GitHubMinAgeDays:         number("TRUST_GH_MIN_AGE_DAYS", 90, 1),
		GitHubMinPublicEvents:    number("TRUST_GH_MIN_PUBLIC_EVENTS", 5, 1),
		GitHubActivityWindowDays: number("TRUST_GH_ACTIVITY_WINDOW_DAYS", 90, 1),
		GitHubMinRepos:           number("TRUST_GH_MIN_REPOS", 3, 1),
		GitHubRepoMinAgeDays:     number("TRUST_GH_REPO_MIN_AGE_DAYS", 30, 1),
		GitHubRecheckDays:        number("TRUST_GH_RECHECK_DAYS", 0, 0),
		VerifyRetryMinutes:       number("TRUST_VERIFY_RETRY_MINUTES", 10, 1),
		ClaimCooldownDays:        number("TRUST_CLAIM_COOLDOWN_DAYS", 30, 0),
		EdgeLimitedMbps:          number("EDGE_LIMITED_MBPS", 1, 1),
		PodLimitedEgressMbps:     number("POD_LIMITED_EGRESS_MBPS", 1, 1),
		GrandfatherBefore:        time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	if raw := strings.TrimSpace(os.Getenv("TRUST_GRANDFATHER_BEFORE")); raw != "" {
		when, err := parseTrustTime(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("TRUST_GRANDFATHER_BEFORE=%q is not a date (2026-11-01) or an RFC 3339 time", raw))
		} else if when.After(time.Now()) {
			errs = append(errs, fmt.Sprintf("TRUST_GRANDFATHER_BEFORE=%q is in the future; accounts created after the backfill ran would be made full", raw))
		} else {
			cfg.GrandfatherBefore = when
		}
	}
	if raw := strings.TrimSpace(os.Getenv("EDGE_LIMITED_ALLOW_WS")); raw != "" {
		allowed, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("EDGE_LIMITED_ALLOW_WS=%q is not true or false", raw))
		}
		cfg.EdgeLimitedAllowWS = allowed
	}
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("limited trial settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

func parseTrustTime(raw string) (time.Time, error) {
	if when, err := time.Parse("2006-01-02", raw); err == nil {
		return when, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func (c AppConfig) validateTrust() error { return c.Trust.err }

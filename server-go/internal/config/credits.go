package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CreditsConfig is every number behind the sign-up credit and admin grants
// (EXC-866). None is decided in code. A mistyped value stops the boot rather
// than falling back, since a silent default would quietly change what people
// are given.
type CreditsConfig struct {
	// TrialDollars is the sign-up credit.
	TrialDollars int
	// TrialDays is how long the sign-up credit lasts.
	TrialDays int
	// TrialCap is how many orgs may hold unexpired sign-up credit at once;
	// later people wait in a first-come queue.
	TrialCap int
	// AdminGrantCapDollars is the most one admin action may add or take away.
	AdminGrantCapDollars int
	// TermsVersion names the credit terms a person accepts before the grant.
	TermsVersion string
	// TermsURL is where Studio links the terms.
	TermsURL string

	err error
}

const (
	defaultCreditTermsVersion = "v1"
	defaultCreditTermsURL     = "https://excalibase.io/credit-terms"
)

func loadCredits() CreditsConfig {
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
	cfg := CreditsConfig{
		TrialDollars:         number("CREDITS_TRIAL_DOLLARS", 300, 1),
		TrialDays:            number("CREDITS_TRIAL_DAYS", 90, 1),
		TrialCap:             number("CREDITS_TRIAL_CAP", 40, 1),
		AdminGrantCapDollars: number("CREDITS_ADMIN_GRANT_CAP_DOLLARS", 1000, 1),
		TermsVersion:         envOr("CREDITS_TERMS_VERSION", defaultCreditTermsVersion),
		TermsURL:             envOr("CREDITS_TERMS_URL", defaultCreditTermsURL),
	}
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("credit settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

func (c AppConfig) validateCredits() error { return c.Credits.err }

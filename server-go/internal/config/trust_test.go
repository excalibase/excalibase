package config

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

var trustEnvs = []string{"TRUST_GH_MIN_AGE_DAYS", "TRUST_GH_MIN_PUBLIC_EVENTS", "TRUST_GH_ACTIVITY_WINDOW_DAYS",
	"TRUST_GH_MIN_REPOS", "TRUST_GH_REPO_MIN_AGE_DAYS", "TRUST_GH_RECHECK_DAYS", "TRUST_VERIFY_RETRY_MINUTES",
	"TRUST_GRANDFATHER_BEFORE", "TRUST_CLAIM_COOLDOWN_DAYS", "EDGE_LIMITED_MBPS", "EDGE_LIMITED_ALLOW_WS", "POD_LIMITED_EGRESS_MBPS"}

func clearTrustEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, env := range trustEnvs {
		t.Setenv(env, "")
	}
}

func TestTrustDefaultsAreTheDecidedValues(t *testing.T) {
	clearTrustEnv(t)
	got := Load().Trust
	want := TrustConfig{
		GitHubMinAgeDays: 90, GitHubMinPublicEvents: 5, GitHubActivityWindowDays: 90,
		GitHubMinRepos: 3, GitHubRepoMinAgeDays: 30, GitHubRecheckDays: 0, VerifyRetryMinutes: 10,
		GrandfatherBefore:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		ClaimCooldownDays:  30,
		EdgeLimitedMbps:    1,
		EdgeLimitedAllowWS: false, PodLimitedEgressMbps: 1,
	}
	got.err = nil
	if got != want {
		t.Errorf("defaults:\n got %+v\nwant %+v", got, want)
	}
	if err := Load().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestTrustEnvOverrides(t *testing.T) {
	clearTrustEnv(t)
	t.Setenv("TRUST_GH_MIN_AGE_DAYS", "120")
	t.Setenv("EDGE_LIMITED_MBPS", "2")
	t.Setenv("EDGE_LIMITED_ALLOW_WS", "true")
	t.Setenv("TRUST_GRANDFATHER_BEFORE", "2026-10-15")
	t.Setenv("TRUST_GH_RECHECK_DAYS", "7")
	got := Load().Trust
	if got.GitHubMinAgeDays != 120 || got.EdgeLimitedMbps != 2 || !got.EdgeLimitedAllowWS || got.GitHubRecheckDays != 7 {
		t.Errorf("overrides not read: %+v", got)
	}
	if !got.GrandfatherBefore.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("grandfather date: %v", got.GrandfatherBefore)
	}
}

// A mistyped value must stop the boot; a silent fallback to a default would
// quietly loosen or tighten who is limited.
func TestTrustRejectsBadValues(t *testing.T) {
	bad := map[string][2]string{
		"not a number":      {"TRUST_GH_MIN_AGE_DAYS", "ninety"},
		"zero age":          {"TRUST_GH_MIN_AGE_DAYS", "0"},
		"negative events":   {"TRUST_GH_MIN_PUBLIC_EVENTS", "-1"},
		"zero window":       {"TRUST_GH_ACTIVITY_WINDOW_DAYS", "0"},
		"zero edge mbps":    {"EDGE_LIMITED_MBPS", "0"},
		"zero pod mbps":     {"POD_LIMITED_EGRESS_MBPS", "0"},
		"negative recheck":  {"TRUST_GH_RECHECK_DAYS", "-1"},
		"negative cooldown": {"TRUST_CLAIM_COOLDOWN_DAYS", "-3"},
		"bad date":          {"TRUST_GRANDFATHER_BEFORE", "yesterday"},
		"cutoff in future":  {"TRUST_GRANDFATHER_BEFORE", "2999-01-01"},
		"bad bool":          {"EDGE_LIMITED_ALLOW_WS", "maybe"},
		"zero retry":        {"TRUST_VERIFY_RETRY_MINUTES", "0"},
	}
	for name, kv := range bad {
		t.Run(name, func(t *testing.T) {
			clearTrustEnv(t)
			t.Setenv(kv[0], kv[1])
			if err := Load().Validate(); err == nil {
				t.Errorf("%s=%s was accepted", kv[0], kv[1])
			}
		})
	}
}

func TestLimitedTrialFlagIsOffUnlessExactlyTrue(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for value, want := range map[string]bool{"": false, "false": false, "yes": false, "TRUE": false, "true": true} {
		t.Setenv("FEATURE_LIMITEDTRIAL", value)
		if got := Load().Features().Enabled(context.Background(), features.LimitedTrial); got != want {
			t.Errorf("FEATURE_LIMITEDTRIAL=%q: %v, want %v", value, got, want)
		}
	}
}

package config

import "testing"

var creditEnvs = []string{"CREDITS_TRIAL_DOLLARS", "CREDITS_TRIAL_DAYS", "CREDITS_TRIAL_CAP", "CREDITS_ADMIN_GRANT_CAP_DOLLARS", "CREDITS_TERMS_VERSION", "CREDITS_TERMS_URL"}

func clearCreditEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, env := range creditEnvs {
		t.Setenv(env, "")
	}
}

func TestCreditDefaultsAreTheDecidedValues(t *testing.T) {
	clearCreditEnv(t)
	got := Load().Credits
	got.err = nil
	want := CreditsConfig{TrialDollars: 300, TrialDays: 90, TrialCap: 40, AdminGrantCapDollars: 1000, TermsVersion: "v1", TermsURL: "https://excalibase.io/credit-terms"}
	if got != want {
		t.Errorf("defaults:\n got %+v\nwant %+v", got, want)
	}
	if err := Load().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestCreditValuesCanBeChangedAndABadOneStopsTheBoot(t *testing.T) {
	clearCreditEnv(t)
	t.Setenv("CREDITS_TRIAL_DOLLARS", "100")
	t.Setenv("CREDITS_TRIAL_CAP", "5")
	if got := Load().Credits; got.TrialDollars != 100 || got.TrialCap != 5 {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []string{"abc", "0", "-3", "1.5"} {
		t.Setenv("CREDITS_TRIAL_DAYS", bad)
		if Load().Validate() == nil {
			t.Errorf("CREDITS_TRIAL_DAYS=%q was accepted", bad)
		}
	}
}

package trust

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestEvaluateTheRule(t *testing.T) {
	cases := []struct {
		name     string
		evidence Evidence
		state    State
		reason   Reason
		says     []string
	}{
		{"old and active by events", Evidence{AccountAgeDays: 400, PublicEvents: 5}, Full, ReasonOK, nil},
		{"old and active by repos", Evidence{AccountAgeDays: 400, PublicRepos: 3, OldestRepoAgeDays: 31}, Full, ReasonOK, nil},
		{"exactly the age", Evidence{AccountAgeDays: 90, PublicEvents: 5}, Full, ReasonOK, nil},
		{"a day too new", Evidence{AccountAgeDays: 89, PublicEvents: 50}, Limited, ReasonGitHubTooNew, []string{"89 days old", "at least 90"}},
		{"old but quiet", Evidence{AccountAgeDays: 400, PublicEvents: 4, PublicRepos: 2}, Limited, ReasonGitHubInactive, []string{"4 public events", "need 5", "2 public repositories", "need 3"}},
		{"enough repos but all young", Evidence{AccountAgeDays: 400, PublicRepos: 5, OldestRepoAgeDays: 29}, Limited, ReasonGitHubInactive, []string{"older than 30 days"}},
		{"new and quiet names both", Evidence{AccountAgeDays: 3}, Limited, ReasonGitHubTooNew, []string{"3 days old", "public events"}},
	}
	for _, tc := range cases {
		got := testPolicy.Evaluate(tc.evidence)
		if got.State != tc.state || got.Reason != tc.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, got.State, got.Reason, tc.state, tc.reason)
		}
		said := strings.Join(got.Missing, " ")
		for _, want := range tc.says {
			if !strings.Contains(said, want) {
				t.Errorf("%s: the explanation %q does not say %q", tc.name, said, want)
			}
		}
		if tc.state == Full && len(got.Missing) != 0 {
			t.Errorf("%s: a pass explained a failure: %v", tc.name, got.Missing)
		}
	}
}

type fakeProfile struct {
	evidence Evidence
	err      error
	calls    int
	token    string
}

func (f *fakeProfile) Evidence(_ context.Context, token string) (Evidence, error) {
	f.calls++
	f.token = token
	return f.evidence, f.err
}

type fakeVerifications struct {
	last      *Attempt
	current   *Record
	recorded  []Attempt
	claimErr  error
	recordErr error
}

func (f *fakeVerifications) Latest(context.Context, string) (*Attempt, error) { return f.last, nil }

func (f *fakeVerifications) Current(context.Context, string) (*Record, error) { return f.current, nil }

func (f *fakeVerifications) Record(_ context.Context, attempt Attempt) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	if attempt.Passed && f.claimErr != nil {
		attempt.Passed, attempt.Reason = false, ReasonGitHubAlreadyUsed
		f.recorded = append(f.recorded, attempt)
		return f.claimErr
	}
	f.recorded = append(f.recorded, attempt)
	return nil
}

var verifyNow = time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)

func newVerifier(profile *fakeProfile, store *fakeVerifications, flags features.Flags) *Verifier {
	v := NewVerifier(flags, profile, store, testPolicy, 10*time.Minute)
	v.now = func() time.Time { return verifyNow }
	return v
}

func TestVerifyPassesAndRecordsTheEvidence(t *testing.T) {
	profile := &fakeProfile{evidence: Evidence{GitHubID: "4242", AccountAgeDays: 500, PublicEvents: 9}}
	store := &fakeVerifications{}
	attempt, err := newVerifier(profile, store, on()).Complete(context.Background(), "u1", "tok")
	if err != nil || !attempt.Passed || attempt.Reason != ReasonOK {
		t.Fatalf("got %+v %v", attempt, err)
	}
	if len(store.recorded) != 1 || store.recorded[0].UserID != "u1" || store.recorded[0].Evidence.GitHubID != "4242" || !store.recorded[0].CheckedAt.Equal(verifyNow) {
		t.Errorf("recorded %+v", store.recorded)
	}
	if profile.token != "tok" {
		t.Errorf("the user's token was not used: %q", profile.token)
	}
}

func TestVerifyFailureSaysWhatIsMissingAndIsRecorded(t *testing.T) {
	profile := &fakeProfile{evidence: Evidence{GitHubID: "4242", AccountAgeDays: 12}}
	store := &fakeVerifications{}
	attempt, err := newVerifier(profile, store, on()).Complete(context.Background(), "u1", "tok")
	if err != nil || attempt.Passed || attempt.Reason != ReasonGitHubTooNew || len(attempt.Missing) == 0 {
		t.Fatalf("got %+v %v", attempt, err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Passed {
		t.Errorf("recorded %+v", store.recorded)
	}
}

func TestVerifyOneGitHubAccountOnlyVerifiesOneAccount(t *testing.T) {
	store := &fakeVerifications{claimErr: ErrGitHubClaimed}
	profile := &fakeProfile{evidence: Evidence{GitHubID: "4242", AccountAgeDays: 500, PublicEvents: 9}}
	attempt, err := newVerifier(profile, store, on()).Complete(context.Background(), "u2", "tok")
	if err != nil || attempt.Passed || attempt.Reason != ReasonGitHubAlreadyUsed {
		t.Fatalf("got %+v %v", attempt, err)
	}
	if !strings.Contains(strings.Join(attempt.Missing, " "), "already used") {
		t.Errorf("the refusal does not say why: %v", attempt.Missing)
	}
}

func TestVerifyACheckThatCannotRunIsRecordedAsSuchNotAsAFailure(t *testing.T) {
	profile := &fakeProfile{err: errors.New("github down")}
	store := &fakeVerifications{}
	attempt, err := newVerifier(profile, store, on()).Complete(context.Background(), "u1", "tok")
	if err != nil || attempt.Passed || attempt.Reason != ReasonCheckFailed {
		t.Fatalf("got %+v %v", attempt, err)
	}
	if strings.Contains(strings.Join(attempt.Missing, " "), "github down") {
		t.Error("the cause leaked to the user")
	}
}

func TestBeginRefusals(t *testing.T) {
	ctx := context.Background()
	if err := newVerifier(&fakeProfile{}, &fakeVerifications{}, off()).Begin(ctx, "u1", true); !errors.Is(err, ErrVerifyOff) {
		t.Errorf("flag off: %v", err)
	}
	if err := newVerifier(&fakeProfile{}, &fakeVerifications{}, on()).Begin(ctx, "u1", false); !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("unverified email: %v", err)
	}
	full := &fakeVerifications{current: &Record{UserID: "u1", State: Full, Method: MethodGrandfathered}}
	if err := newVerifier(&fakeProfile{}, full, on()).Begin(ctx, "u1", true); !errors.Is(err, ErrAlreadyVerified) {
		t.Errorf("already full: %v", err)
	}
	held := &fakeVerifications{current: &Record{UserID: "u1", State: Limited, Method: MethodAdmin}}
	if err := newVerifier(&fakeProfile{}, held, on()).Begin(ctx, "u1", true); !errors.Is(err, ErrAdminHold) {
		t.Errorf("admin hold: %v", err)
	}
	recent := &fakeVerifications{last: &Attempt{CheckedAt: verifyNow.Add(-3 * time.Minute)}}
	err := newVerifier(&fakeProfile{}, recent, on()).Begin(ctx, "u1", true)
	var soon *TooSoonError
	if !errors.As(err, &soon) || soon.RetryAfter != 7*time.Minute {
		t.Errorf("rate limit: %v", err)
	}
	old := &fakeVerifications{last: &Attempt{CheckedAt: verifyNow.Add(-11 * time.Minute)}}
	if err := newVerifier(&fakeProfile{}, old, on()).Begin(ctx, "u1", true); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}

func TestCompleteIsAlsoRateLimitedAndFlagGated(t *testing.T) {
	profile := &fakeProfile{evidence: Evidence{AccountAgeDays: 500, PublicEvents: 9}}
	recent := &fakeVerifications{last: &Attempt{CheckedAt: verifyNow.Add(-time.Minute)}}
	if _, err := newVerifier(profile, recent, on()).Complete(context.Background(), "u1", "tok"); err == nil || profile.calls != 0 {
		t.Errorf("a callback inside the wait still called GitHub: %v calls=%d", err, profile.calls)
	}
	if _, err := newVerifier(profile, &fakeVerifications{}, off()).Complete(context.Background(), "u1", "tok"); !errors.Is(err, ErrVerifyOff) || profile.calls != 0 {
		t.Errorf("flag off: %v calls=%d", err, profile.calls)
	}
}

func TestExplainRebuildsTheReasonFromStoredEvidence(t *testing.T) {
	if got := testPolicy.Explain(true, ReasonOK, Evidence{}); len(got) != 0 {
		t.Errorf("a pass explained %v", got)
	}
	if got := testPolicy.Explain(false, ReasonGitHubTooNew, Evidence{AccountAgeDays: 5, PublicEvents: 9}); len(got) != 1 || !strings.Contains(got[0], "5 days old") {
		t.Errorf("too new: %v", got)
	}
	if got := testPolicy.Explain(false, ReasonGitHubAlreadyUsed, Evidence{}); len(got) != 1 || !strings.Contains(got[0], "already used") {
		t.Errorf("already used: %v", got)
	}
	if got := testPolicy.Explain(false, ReasonCheckFailed, Evidence{}); len(got) != 1 || !strings.Contains(got[0], "try again") {
		t.Errorf("check failed: %v", got)
	}
}

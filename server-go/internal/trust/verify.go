package trust

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// RuleVersion is stored with each decision, so a rule change can be told apart.
const RuleVersion = 1

// ClaimKey is the key one GitHub account holds on an Excalibase account. The
// credits grant uses the same key, so one GitHub account earns one of each.
func ClaimKey(githubID string) string { return "github:" + githubID }

var (
	ErrVerifyOff        = errors.New("trust: verification is not switched on")
	ErrEmailNotVerified = errors.New("verify your email address first, then verify with GitHub")
	ErrAlreadyVerified  = errors.New("this account is already verified")
	ErrAdminHold        = errors.New("an administrator has limited this account; contact support")
	ErrGitHubClaimed    = errors.New("this GitHub account already verified another Excalibase account")
)

// TooSoonError is a verify attempt inside the wait between two.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return fmt.Sprintf("verification was tried a moment ago; try again in %d minutes", int((e.RetryAfter+time.Minute-1)/time.Minute))
}

// Evidence is what the rule is judged on: counts and ages, no GitHub content.
type Evidence struct {
	GitHubID          string
	AccountAgeDays    int
	PublicEvents      int
	PublicRepos       int
	OldestRepoAgeDays int
}

// Outcome is the rule's verdict, with a sentence for each thing that is missing.
type Outcome struct {
	State   State
	Reason  Reason
	Missing []string
}

// Evaluate judges evidence against the configured rule: the account is old
// enough and shows activity, either public events in the window or public
// repositories, one of them old enough.
func (p Policy) Evaluate(e Evidence) Outcome {
	var missing []string
	reason := ReasonOK
	if e.AccountAgeDays < p.MinAgeDays {
		missing = append(missing, fmt.Sprintf("Your GitHub account is %d days old; it must be at least %d.", e.AccountAgeDays, p.MinAgeDays))
		reason = ReasonGitHubTooNew
	}
	byEvents := e.PublicEvents >= p.MinPublicEvents
	byRepos := e.PublicRepos >= p.MinRepos && e.OldestRepoAgeDays >= p.RepoMinAgeDays
	if !byEvents && !byRepos {
		missing = append(missing, fmt.Sprintf(
			"It needs recent activity: it has %d public events in the last %d days (need %d), or %d public repositories (need %d, one of them older than %d days).",
			e.PublicEvents, p.ActivityWindowDays, p.MinPublicEvents, e.PublicRepos, p.MinRepos, p.RepoMinAgeDays))
		if reason == ReasonOK {
			reason = ReasonGitHubInactive
		}
	}
	if len(missing) > 0 {
		return Outcome{State: Limited, Reason: reason, Missing: missing}
	}
	return Outcome{State: Full, Reason: ReasonOK}
}

const (
	messageClaimed = "This GitHub account is already used to verify another Excalibase account. Use a different GitHub account."
	messageUnread  = "GitHub could not be checked just now; try again in a few minutes."
)

// Explain says why an attempt ended as it did, from what is stored: the rule
// is re-judged on the evidence, so nothing but numbers has to be kept.
func (p Policy) Explain(passed bool, reason Reason, e Evidence) []string {
	switch {
	case passed:
		return nil
	case reason == ReasonGitHubAlreadyUsed:
		return []string{messageClaimed}
	case reason == ReasonCheckFailed:
		return []string{messageUnread}
	}
	return p.Evaluate(e).Missing
}

// Attempt is one verification and why it ended as it did; the latest per
// account is what Studio reads back.
type Attempt struct {
	UserID    string
	CheckedAt time.Time
	Evidence  Evidence
	Passed    bool
	Reason    Reason
	// Missing is the explanation shown to the account; it is not stored.
	Missing []string
}

// ProfileSource reads the evidence from GitHub with the user's own token.
type ProfileSource interface {
	Evidence(ctx context.Context, accessToken string) (Evidence, error)
}

// VerificationStore keeps attempts. Record writes the attempt, and for a pass
// the claim on the GitHub account and the full decision, in one transaction;
// a claim held by another account stores a failed attempt and returns ErrGitHubClaimed.
type VerificationStore interface {
	Latest(ctx context.Context, userID string) (*Attempt, error)
	Current(ctx context.Context, userID string) (*Record, error)
	Record(ctx context.Context, attempt Attempt) error
}

// Verifier turns a limited account into a full one when its GitHub account passes the rule.
type Verifier struct {
	flags   features.Flags
	profile ProfileSource
	store   VerificationStore
	policy  Policy
	wait    time.Duration
	now     func() time.Time
}

func NewVerifier(flags features.Flags, profile ProfileSource, store VerificationStore, policy Policy, wait time.Duration) *Verifier {
	return &Verifier{flags: flags, profile: profile, store: store, policy: policy, wait: wait, now: time.Now}
}

// Begin is asked before the account is sent to GitHub.
func (v *Verifier) Begin(ctx context.Context, userID string, emailVerified bool) error {
	if !features.Enabled(ctx, v.flags, features.LimitedTrial) {
		return ErrVerifyOff
	}
	if !emailVerified {
		return ErrEmailNotVerified
	}
	current, err := v.store.Current(ctx, userID)
	if err != nil {
		return fmt.Errorf("trust: read decision: %w", err)
	}
	if current != nil && current.State == Full {
		return ErrAlreadyVerified
	}
	if current != nil && current.Method == MethodAdmin {
		return ErrAdminHold
	}
	return v.checkWait(ctx, userID)
}

func (v *Verifier) checkWait(ctx context.Context, userID string) error {
	last, err := v.store.Latest(ctx, userID)
	if err != nil {
		return fmt.Errorf("trust: read last attempt: %w", err)
	}
	if last == nil {
		return nil
	}
	if left := last.CheckedAt.Add(v.wait).Sub(v.now()); left > 0 {
		return &TooSoonError{RetryAfter: left}
	}
	return nil
}

// Complete reads the GitHub evidence with the user's token, judges it and
// records the result. A check that could not run is recorded as such; the
// error is only for what stopped it from being recorded.
func (v *Verifier) Complete(ctx context.Context, userID, accessToken string) (Attempt, error) {
	if !features.Enabled(ctx, v.flags, features.LimitedTrial) {
		return Attempt{}, ErrVerifyOff
	}
	if err := v.checkWait(ctx, userID); err != nil {
		return Attempt{}, err
	}
	attempt := Attempt{UserID: userID, CheckedAt: v.now().UTC()}
	evidence, err := v.profile.Evidence(ctx, accessToken)
	switch {
	case err != nil || evidence.GitHubID == "":
		attempt.Reason = ReasonCheckFailed
		attempt.Missing = []string{messageUnread}
	default:
		outcome := v.policy.Evaluate(evidence)
		attempt.Evidence, attempt.Passed, attempt.Reason, attempt.Missing = evidence, outcome.State == Full, outcome.Reason, outcome.Missing
	}
	if err := v.store.Record(ctx, attempt); err != nil {
		if !errors.Is(err, ErrGitHubClaimed) {
			return Attempt{}, fmt.Errorf("trust: record attempt: %w", err)
		}
		attempt.Passed, attempt.Reason = false, ReasonGitHubAlreadyUsed
		attempt.Missing = []string{messageClaimed}
	}
	return attempt, nil
}

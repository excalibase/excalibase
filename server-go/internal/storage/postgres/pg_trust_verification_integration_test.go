//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

const claimCooldownDays = 30

func verifications(store *Store) trust.VerificationStore {
	return store.Verifications(claimCooldownDays)
}

func passing(userID, githubID string) trust.Attempt {
	return trust.Attempt{UserID: userID, CheckedAt: time.Now().UTC(), Passed: true, Reason: trust.ReasonOK,
		Evidence: trust.Evidence{GitHubID: githubID, AccountAgeDays: 400, PublicEvents: 9, PublicRepos: 4, OldestRepoAgeDays: 120}}
}

func newUsers(t *testing.T, store *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := store.CreateUser(context.Background(), userNamed(id, id)); err != nil {
			t.Fatal(err)
		}
	}
}

func claimRow(t *testing.T, store *Store, key string) (user *string, released *time.Time) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT user_id, released_at FROM trust_claims WHERE claim_key = $1`, key).Scan(&user, &released); err != nil {
		t.Fatalf("claim %s: %v", key, err)
	}
	return user, released
}

func TestVerificationPassMakesTheAccountFullAndClaimsTheGitHubAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "v1")
	if err := verifications(store).Record(ctx, passing("v1", "4242")); err != nil {
		t.Fatal(err)
	}
	got, _ := store.FindUserTrust(ctx, "v1")
	if got == nil || got.State != trust.Full || got.Method != trust.MethodGitHub || got.Reason != trust.ReasonOK || got.CheckVersion != trust.RuleVersion {
		t.Fatalf("decision %+v", got)
	}
	if user, released := claimRow(t, store, trust.ClaimKey("4242")); user == nil || *user != "v1" || released != nil {
		t.Errorf("claim %v %v", user, released)
	}
	latest, err := verifications(store).Latest(ctx, "v1")
	if err != nil || latest == nil || !latest.Passed || latest.Evidence.GitHubID != "4242" || latest.Evidence.AccountAgeDays != 400 ||
		latest.Evidence.PublicEvents != 9 || latest.Evidence.PublicRepos != 4 || latest.Evidence.OldestRepoAgeDays != 120 || latest.CheckedAt.IsZero() {
		t.Errorf("latest %+v %v", latest, err)
	}
	if audit := auditRows(t, store, "v1"); len(audit) != 1 || audit[0][1] != "full" {
		t.Errorf("audit %v", audit)
	}
}

func TestVerificationFailureKeepsTheAccountLimitedAndClaimsNothing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "v2")
	fail := trust.Attempt{UserID: "v2", CheckedAt: time.Now().UTC(), Reason: trust.ReasonGitHubTooNew, Evidence: trust.Evidence{GitHubID: "5", AccountAgeDays: 3}}
	if err := verifications(store).Record(ctx, fail); err != nil {
		t.Fatal(err)
	}
	got, _ := store.FindUserTrust(ctx, "v2")
	if got == nil || got.State != trust.Limited || got.Reason != trust.ReasonGitHubTooNew {
		t.Fatalf("decision %+v", got)
	}
	var claims int
	_ = store.db.QueryRow(`SELECT count(*) FROM trust_claims WHERE claim_key = $1`, trust.ClaimKey("5")).Scan(&claims)
	if claims != 0 {
		t.Errorf("a failed attempt claimed the GitHub account")
	}
	if latest, _ := verifications(store).Latest(ctx, "v2"); latest == nil || latest.Passed || latest.Reason != trust.ReasonGitHubTooNew {
		t.Errorf("latest %+v", latest)
	}
}

func TestOneGitHubAccountVerifiesOnlyOneAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "first", "second")
	if err := verifications(store).Record(ctx, passing("first", "777")); err != nil {
		t.Fatal(err)
	}
	if err := verifications(store).Record(ctx, passing("second", "777")); !errors.Is(err, trust.ErrGitHubClaimed) {
		t.Fatalf("second account: %v, want ErrGitHubClaimed", err)
	}
	second, _ := store.FindUserTrust(ctx, "second")
	if second == nil || second.State != trust.Limited || second.Reason != trust.ReasonGitHubAlreadyUsed {
		t.Errorf("second %+v", second)
	}
	if first, _ := store.FindUserTrust(ctx, "first"); first == nil || first.State != trust.Full {
		t.Errorf("first %+v", first)
	}
	if err := verifications(store).Record(ctx, passing("first", "777")); !errors.Is(err, trust.ErrAlreadyVerified) {
		t.Errorf("the holder verifying again: %v", err)
	}
}

func TestTenAccountsRacingForOneGitHubAccountLeaveOneFull(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	ids := []string{"r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9"}
	newUsers(t, store, ids...)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = verifications(store).Record(ctx, passing(id, "888"))
		}()
	}
	wg.Wait()
	var full int
	_ = store.db.QueryRow(`SELECT count(*) FROM user_trust WHERE state = 'full' AND user_id LIKE 'r_'`).Scan(&full)
	if full != 1 {
		t.Fatalf("%d accounts are full, want exactly 1", full)
	}
}

// A claim is released when its account is deleted, and stays closed to others for the cooldown.
func TestAReleasedClaimIsClosedForTheCooldown(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "gone", "later")
	if err := verifications(store).Record(ctx, passing("gone", "999")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM users WHERE id = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if user, released := claimRow(t, store, trust.ClaimKey("999")); user != nil || released == nil {
		t.Fatalf("after the delete: %v %v", user, released)
	}
	if err := verifications(store).Record(ctx, passing("later", "999")); !errors.Is(err, trust.ErrGitHubClaimed) {
		t.Fatalf("inside the cooldown: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE trust_claims SET released_at = now() - interval '31 days' WHERE claim_key = $1`, trust.ClaimKey("999")); err != nil {
		t.Fatal(err)
	}
	if err := verifications(store).Record(ctx, passing("later", "999")); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
}

func TestRecordNeverLowersAFullAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "keep")
	if err := store.SaveUserTrust(ctx, trustRecord("keep", trust.Full, trust.MethodGrandfathered, trust.ReasonGrandfathered), "backfill"); err != nil {
		t.Fatal(err)
	}
	fail := trust.Attempt{UserID: "keep", CheckedAt: time.Now().UTC(), Reason: trust.ReasonGitHubInactive, Evidence: trust.Evidence{GitHubID: "6"}}
	if err := verifications(store).Record(ctx, fail); !errors.Is(err, trust.ErrAlreadyVerified) {
		t.Fatalf("got %v", err)
	}
	if got, _ := store.FindUserTrust(ctx, "keep"); got.State != trust.Full || got.Method != trust.MethodGrandfathered {
		t.Errorf("a full account was changed: %+v", got)
	}
}

func TestVerifyOAuthStateKeepsItsPurposeAndAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	state := domain.OAuthState{Provider: "github", CodeVerifier: "cv", Purpose: "verify", UserID: "u-9"}
	if err := store.SaveOAuthState(ctx, "hash-1", state, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := store.ConsumeOAuthState(ctx, "hash-1", time.Now())
	if err != nil || *got != state {
		t.Fatalf("got %+v %v", got, err)
	}
}

// An account whose decision was lowered later (a re-check) can take its own GitHub claim again.
func TestTheHolderOfAClaimCanVerifyAgainAfterItsDecisionWasLowered(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "holder")
	if err := verifications(store).Record(ctx, passing("holder", "1313")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("holder", trust.Limited, trust.MethodGitHub, trust.ReasonGitHubInactive), "recheck"); err != nil {
		t.Fatal(err)
	}
	if err := verifications(store).Record(ctx, passing("holder", "1313")); err != nil {
		t.Fatalf("the holder was refused its own claim: %v", err)
	}
	if got, _ := store.FindUserTrust(ctx, "holder"); got.State != trust.Full {
		t.Errorf("decision %+v", got)
	}
}

// An administrator's hold set between Begin and Record is met inside the locked
// transaction: a pass does not lift it and a failure does not overwrite it.
func TestRecordRefusesAnAdminHoldAndLeavesItAlone(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	newUsers(t, store, "held")
	if err := store.SaveUserTrust(ctx, trustRecord("held", trust.Limited, trust.MethodAdmin, trust.ReasonAdminOverride), "admin-1"); err != nil {
		t.Fatal(err)
	}
	fail := trust.Attempt{UserID: "held", CheckedAt: time.Now().UTC(), Reason: trust.ReasonGitHubInactive, Evidence: trust.Evidence{GitHubID: "2020"}}
	for name, attempt := range map[string]trust.Attempt{"a pass": passing("held", "2020"), "a failure": fail} {
		if err := verifications(store).Record(ctx, attempt); !errors.Is(err, trust.ErrAdminHold) {
			t.Fatalf("%s: err = %v, want ErrAdminHold", name, err)
		}
		got, _ := store.FindUserTrust(ctx, "held")
		if got.State != trust.Limited || got.Method != trust.MethodAdmin || got.Reason != trust.ReasonAdminOverride {
			t.Fatalf("%s overwrote the hold: %+v", name, got)
		}
	}
	var claims int
	_ = store.db.QueryRow(`SELECT count(*) FROM trust_claims WHERE claim_key = $1`, trust.ClaimKey("2020")).Scan(&claims)
	if claims != 0 {
		t.Error("a held account claimed a GitHub account")
	}
	if latest, _ := verifications(store).Latest(ctx, "held"); latest != nil {
		t.Errorf("a refused attempt was recorded: %+v", latest)
	}
}

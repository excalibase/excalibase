//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func grant(t *testing.T, store *Store, userID string, trialCap int) credits.GrantResult {
	t.Helper()
	result, err := store.GrantCreditTrial(context.Background(), userID, trialRules(trialCap), creditsNow)
	if err != nil {
		t.Fatalf("grant %s: %v", userID, err)
	}
	return result
}

func TestTrialGrantLandsOnTheFreeOrgForNinetyDays(t *testing.T) {
	store := testStore(t)
	orgID := creditPerson(t, store, "ann")
	result := grant(t, store, "ann", 40)
	if result.Outcome != credits.OutcomeGranted || result.OrgID != orgID {
		t.Fatalf("%+v", result)
	}
	account, _ := store.CreditAccount(context.Background(), orgID)
	if account.Balance != credits.FromDollars(300) || len(account.Lots) != 1 {
		t.Fatalf("%+v", account)
	}
	want := creditsNow.Add(90 * 24 * time.Hour)
	if got := account.Lots[0].ExpiresAt; got == nil || !got.Equal(want) {
		t.Fatalf("expires %v, want %v", got, want)
	}
	var kind, terms string
	if err := store.db.QueryRow(`SELECT e.kind, c.terms_version FROM credit_grant_claims c JOIN credit_entries e ON e.id = c.entry_id WHERE c.claim_kind = 'user' AND c.claim_value = 'ann'`).Scan(&kind, &terms); err != nil || kind != "grant_trial" || terms != creditsTerms {
		t.Fatalf("claim: %s %s %v", kind, terms, err)
	}
	creditInvariant(t, store, orgID)
}

func TestTenConcurrentGrantsForOnePersonGiveOneEntry(t *testing.T) {
	store := testStore(t)
	orgID := creditPerson(t, store, "bob")
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.GrantCreditTrial(context.Background(), "bob", trialRules(40), creditsNow); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var entries int
	_ = store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = $1`, orgID).Scan(&entries)
	if entries != 1 {
		t.Fatalf("%d entries, want 1", entries)
	}
	if again := grant(t, store, "bob", 40); again.Outcome != credits.OutcomeAlreadyGranted {
		t.Fatalf("a replay: %+v", again)
	}
}

func TestGrantNeedsVerifiedTermsActiveAndAFreeOrg(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "cat")

	if err := store.CreateUser(ctx, userNamed("dan", "dan")); err != nil {
		t.Fatal(err)
	}
	if got := grant(t, store, "dan", 40).Outcome; got != credits.OutcomeNotEligible {
		t.Errorf("unverified: %s", got)
	}
	_ = store.MarkEmailVerified(ctx, "dan", creditsNow)
	if got := grant(t, store, "dan", 40).Outcome; got != credits.OutcomeNeedsTerms {
		t.Errorf("terms not accepted: %s", got)
	}
	_ = store.AcceptCreditTerms(ctx, "dan", "an-old-version", creditsNow)
	if got := grant(t, store, "dan", 40).Outcome; got != credits.OutcomeNeedsTerms {
		t.Errorf("an old terms version: %s", got)
	}
	_ = store.AcceptCreditTerms(ctx, "dan", creditsTerms, creditsNow)
	if got := grant(t, store, "dan", 40).Outcome; got != credits.OutcomeNoFreeOrg {
		t.Errorf("no free org: %s", got)
	}

	if _, err := store.db.Exec(`UPDATE users SET active = false WHERE id = 'cat'`); err != nil {
		t.Fatal(err)
	}
	if got := grant(t, store, "cat", 40).Outcome; got != credits.OutcomeNotEligible {
		t.Errorf("suspended: %s", got)
	}
	_, _ = store.db.Exec(`UPDATE users SET active = true WHERE id = 'cat'`)
	if _, err := store.db.Exec(`INSERT INTO abuse_suspensions (user_id) VALUES ('cat')`); err != nil {
		t.Fatal(err)
	}
	if got := grant(t, store, "cat", 40).Outcome; got != credits.OutcomeNotEligible {
		t.Errorf("abuse suspension: %s", got)
	}
}

func TestAnIdentityLinkedAfterAGrantBlocksASecondAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "eve")
	creditPerson(t, store, "fay")
	grant(t, store, "eve", 40)

	if err := store.LinkStudioIdentity(ctx, "github", "gh-42", "eve", "eve@x.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkStudioIdentity(ctx, "google", "g-7", "eve", "eve@x.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkStudioIdentity(ctx, "github", "gh-42", "fay", "fay@x.example.com"); err == nil {
		t.Fatal("a subject already linked cannot be linked twice")
	}
	if err := store.LinkStudioIdentity(ctx, "google", "g-8", "fay", "fay@x.example.com"); err != nil {
		t.Fatal(err)
	}
	var claims int
	_ = store.db.QueryRow(`SELECT count(*) FROM credit_grant_claims WHERE user_id = 'eve'`).Scan(&claims)
	if claims != 4 {
		t.Fatalf("eve holds %d claims, want user + email + github + google", claims)
	}
}

func TestASubjectClaimedByAnotherPersonGivesNoGrantAndDoesNotBlockTheLink(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "gil")
	creditPerson(t, store, "hal")
	if err := store.LinkStudioIdentity(ctx, "github", "gh-9", "gil", "gil@x.example.com"); err != nil {
		t.Fatal(err)
	}
	grant(t, store, "gil", 40)
	if _, err := store.db.Exec(`DELETE FROM studio_identities WHERE user_id = 'gil'`); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkStudioIdentity(ctx, "github", "gh-9", "hal", "hal@x.example.com"); err != nil {
		t.Fatalf("a claimed subject must not block the link: %v", err)
	}
	if got := grant(t, store, "hal", 40); got.Outcome != credits.OutcomeIdentityClaimed {
		t.Fatalf("the second account got %+v", got)
	}
	var entries int
	_ = store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = 'org-hal'`).Scan(&entries)
	if entries != 0 {
		t.Fatal("a refused grant wrote an entry")
	}
	if got := grant(t, store, "hal", 40); got.Outcome != credits.OutcomeIdentityClaimed {
		t.Fatalf("a second attempt: %+v", got)
	}
}

func expectOutcome(t *testing.T, store *Store, userID string, want credits.GrantOutcome, why string) {
	t.Helper()
	if got := grant(t, store, userID, 2).Outcome; got != want {
		t.Fatalf("%s: %s got %v, want %v", why, userID, got, want)
	}
}

func expectQueue(t *testing.T, store *Store, want ...string) {
	t.Helper()
	queue, err := store.CreditWaitlist(context.Background(), 10)
	if err != nil || !slices.Equal(queue, want) {
		t.Fatalf("queue %v (%v), want %v", queue, err, want)
	}
}

func TestTrialCapWaitlistsAndGrantsFirstComeWhenATrialEnds(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"k1", "k2", "k3", "k4"} {
		creditPerson(t, store, id)
	}
	expectOutcome(t, store, "k1", credits.OutcomeGranted, "the first two fit under the cap")
	expectOutcome(t, store, "k2", credits.OutcomeGranted, "the first two fit under the cap")
	expectOutcome(t, store, "k3", credits.OutcomeWaitlisted, "past the cap people wait")
	expectOutcome(t, store, "k4", credits.OutcomeWaitlisted, "past the cap people wait")
	expectOutcome(t, store, "k3", credits.OutcomeWaitlisted, "asking again keeps the place")
	expectQueue(t, store, "k3", "k4")
	if state, _ := store.CreditTrialState(ctx, "k4"); state.State != "waitlisted" || state.Position != 2 {
		t.Fatalf("%+v", state)
	}
	expectOutcome(t, store, "k3", credits.OutcomeWaitlisted, "still full")

	if n, err := store.ExpireCreditLots(ctx, creditsNow.Add(91*24*time.Hour), false); err != nil || n != 2 {
		t.Fatalf("both trials end: %d %v", n, err)
	}
	expectOutcome(t, store, "k3", credits.OutcomeGranted, "a place opened")
	expectQueue(t, store, "k4")
}

func TestPendingTrialUsersAreVerifiedAcceptedAndNotYetClaimed(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "p1")
	creditPerson(t, store, "p2")
	creditPerson(t, store, "p3")
	grant(t, store, "p1", 40)
	_ = store.CreateUser(ctx, userNamed("p4", "p4"))
	pending, err := store.PendingCreditTrialUsers(ctx, creditsTerms, 10)
	if err != nil || len(pending) != 2 || pending[0] != "p2" || pending[1] != "p3" {
		t.Fatalf("%v %v", pending, err)
	}
	grant(t, store, "p2", 1)
	grant(t, store, "p3", 1)
	pending, _ = store.PendingCreditTrialUsers(ctx, creditsTerms, 10)
	if len(pending) != 0 {
		t.Fatalf("a waitlisted person is not asked again by the sweeper: %v", pending)
	}
}

func TestANewcomerCannotJumpTheQueueWhenAPlaceOpens(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"q1", "q2", "q3", "q4"} {
		creditPerson(t, store, id)
	}
	creditPerson(t, store, "q5")
	rules := trialRules(2)
	at := func(person string, when time.Time) credits.GrantOutcome {
		result, err := store.GrantCreditTrial(ctx, person, rules, when)
		if err != nil {
			t.Fatal(err)
		}
		return result.Outcome
	}
	at("q1", creditsNow)
	at("q2", creditsNow.Add(48*time.Hour))
	at("q3", creditsNow.Add(49*time.Hour))
	// q1's credit ends before q2's: one place is free now.
	later := creditsNow.Add(90*24*time.Hour + time.Hour)
	if n, err := store.ExpireCreditLots(ctx, later, false); err != nil || n != 1 {
		t.Fatalf("one trial ends: %d %v", n, err)
	}
	if got := at("q5", later); got != credits.OutcomeWaitlisted {
		t.Fatalf("q3 asked first and waits for the free place, so q5 must queue behind: %s", got)
	}
	if got := at("q3", later); got != credits.OutcomeGranted {
		t.Fatalf("q3 takes the free place: %s", got)
	}
}

// Two queued people who became ineligible must not hold the places of a trial that ended.
func TestIneligibleQueuedPeopleDoNotFreezeTheQueue(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"f1", "f2", "f3", "f4", "f5"} {
		creditPerson(t, store, id)
	}
	rules := trialRules(2)
	at := func(person string, when time.Time) credits.GrantOutcome {
		result, err := store.GrantCreditTrial(ctx, person, rules, when)
		if err != nil {
			t.Fatal(err)
		}
		return result.Outcome
	}
	at("f1", creditsNow)
	at("f2", creditsNow)
	if at("f3", creditsNow) != credits.OutcomeWaitlisted || at("f4", creditsNow) != credits.OutcomeWaitlisted {
		t.Fatal("f3 and f4 queue behind the full cap")
	}
	if _, err := store.db.Exec(`UPDATE users SET active = false WHERE id = 'f3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM orgs WHERE id = 'org-f4'`); err != nil {
		t.Fatal(err)
	}
	later := creditsNow.Add(91 * 24 * time.Hour)
	if n, err := store.ExpireCreditLots(ctx, later, false); err != nil || n != 2 {
		t.Fatalf("both trials end: %d %v", n, err)
	}
	if got := at("f5", later); got != credits.OutcomeGranted {
		t.Fatalf("a newcomer behind two ineligible people must get the free place: %s", got)
	}
}

func TestAPersonFoundIneligibleLeavesTheQueue(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"g1", "g2"} {
		creditPerson(t, store, id)
	}
	if grant(t, store, "g1", 1).Outcome != credits.OutcomeGranted || grant(t, store, "g2", 1).Outcome != credits.OutcomeWaitlisted {
		t.Fatal("setup")
	}
	if _, err := store.db.Exec(`UPDATE orgs SET tier = 'STANDARD' WHERE id = 'org-g2'`); err != nil {
		t.Fatal(err)
	}
	if got := grant(t, store, "g2", 1).Outcome; got != credits.OutcomeNoFreeOrg {
		t.Fatalf("%s", got)
	}
	if queue, _ := store.CreditWaitlist(ctx, 10); len(queue) != 0 {
		t.Fatalf("an ineligible person stays queued: %v", queue)
	}
}

func TestADeletedAccountsAddressCannotEarnASecondGrant(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "h1")
	grant(t, store, "h1", 40)
	for _, statement := range []string{`DELETE FROM org_members WHERE user_id = 'h1'`, `DELETE FROM orgs WHERE id = 'org-h1'`, `DELETE FROM users WHERE id = 'h1'`} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// The same address signs up again as a new account.
	if err := store.CreateUser(ctx, &domain.User{ID: "h2", Username: "h2", Email: "H1@x.example.com", PasswordHash: "x", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	_ = store.MarkEmailVerified(ctx, "h2", creditsNow)
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-h2", "org-h2", "h2", domain.Free), 1); err != nil {
		t.Fatal(err)
	}
	_ = store.AcceptCreditTerms(ctx, "h2", creditsTerms, creditsNow)
	if got := grant(t, store, "h2", 40).Outcome; got != credits.OutcomeIdentityClaimed {
		t.Fatalf("the same address twice: %s", got)
	}
}

// A link that starts while a grant is deciding waits for it, so its claim is never missed.
func TestLinkingAnIdentityWaitsForARunningGrant(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	creditPerson(t, store, "i1")
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, creditGrantLockSpace); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.LinkStudioIdentity(ctx, "github", "gh-race", "i1", "i1@x.example.com") }()
	select {
	case err := <-done:
		t.Fatalf("the link did not wait for the grant lock: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Each way of becoming ineligible, alone, must keep a queued person from holding the only free place.
func TestEveryKindOfIneligibleQueuedPersonIsPassedOver(t *testing.T) {
	cases := map[string]string{
		"deactivated":  `UPDATE users SET active = false WHERE id = 'w2'`,
		"suspended":    `INSERT INTO abuse_suspensions (user_id) VALUES ('w2')`,
		"upgraded":     `UPDATE orgs SET tier = 'STANDARD' WHERE id = 'org-w2'`,
		"unverified":   `UPDATE users SET email_verified_at = NULL WHERE id = 'w2'`,
		"old terms":    `UPDATE credit_terms_acceptances SET terms_version = 'older' WHERE user_id = 'w2'`,
		"already held": `INSERT INTO credit_grant_claims (claim_kind, claim_value, user_id, org_id) VALUES ('user', 'w2', 'w2', 'org-w2')`,
	}
	store := testStore(t)
	ctx := context.Background()
	n := 0
	for name, statement := range cases {
		n++
		_ = name
		first, second, newcomer := fmt.Sprintf("v%da", n), fmt.Sprintf("v%db", n), fmt.Sprintf("v%dc", n)
		for _, id := range []string{first, second, newcomer} {
			creditPerson(t, store, id)
		}
		// each case runs on its own cap-1 world: earlier grants expire first
		_, _ = store.db.Exec(`TRUNCATE credit_waitlist`)
		rules := trialRules(1)
		now := creditsNow.Add(time.Duration(n) * 200 * 24 * time.Hour)
		if r, _ := store.GrantCreditTrial(ctx, first, rules, now); r.Outcome != credits.OutcomeGranted {
			t.Fatalf("%s: first %s", name, r.Outcome)
		}
		if r, _ := store.GrantCreditTrial(ctx, second, rules, now); r.Outcome != credits.OutcomeWaitlisted {
			t.Fatalf("%s: second %s", name, r.Outcome)
		}
		if _, err := store.db.Exec(replaceID(statement, second)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		later := now.Add(91 * 24 * time.Hour)
		if _, err := store.ExpireCreditLots(ctx, later, false); err != nil {
			t.Fatal(err)
		}
		if r, _ := store.GrantCreditTrial(ctx, newcomer, rules, later); r.Outcome != credits.OutcomeGranted {
			t.Fatalf("%s: a newcomer behind an ineligible person got %s", name, r.Outcome)
		}
	}
}

func replaceID(statement, id string) string {
	return strings.NewReplacer("w2", id, "org-w2", "org-"+id).Replace(statement)
}

func TestTheAcceptedTermsVersionIsReadBackAndEmptyForSomeoneWhoNeverAccepted(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if version, err := store.CreditTermsVersion(ctx, "nobody"); err != nil || version != "" {
		t.Fatalf("never accepted: %q %v", version, err)
	}
	if err := store.AcceptCreditTerms(ctx, "reader", "terms-7", creditsNow); err != nil {
		t.Fatal(err)
	}
	if version, err := store.CreditTermsVersion(ctx, "reader"); err != nil || version != "terms-7" {
		t.Fatalf("%q %v", version, err)
	}
	if err := store.AcceptCreditTerms(ctx, "reader", "terms-8", creditsNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if version, _ := store.CreditTermsVersion(ctx, "reader"); version != "terms-8" {
		t.Fatalf("a later acceptance replaces the earlier one: %q", version)
	}
}

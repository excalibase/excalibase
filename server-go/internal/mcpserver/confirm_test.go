package mcpserver

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func userNamed(id string) *domain.User { return &domain.User{ID: id, Active: true} }

func tokenNamed(hash string) *domain.AccessToken {
	return &domain.AccessToken{TokenHash: hash, Name: "laptop", UserID: "user-1"}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) read() time.Time { return c.now }

func newTestConfirmations() (*confirmations, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	store := newConfirmations()
	store.now = clock.read
	return store, clock
}

var (
	userOne = Caller{User: userNamed("user-1"), Token: tokenNamed("hash-1")}
	userTwo = Caller{User: userNamed("user-2"), Token: tokenNamed("hash-2")}
	target  = confirmTarget{project: "proj-a", id: "web"}
)

func TestConfirmationsIssueAThenRedeemOnce(t *testing.T) {
	store, _ := newTestConfirmations()
	token, _, err := store.issue(userOne, "delete_app", target)
	if err != nil || token == "" {
		t.Fatalf("issue = %q, %v", token, err)
	}
	if err := store.redeem(userOne, token, "delete_app", target); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := store.redeem(userOne, token, "delete_app", target); err == nil {
		t.Fatal("a token was redeemed twice")
	}
}

func TestConfirmationsAreRandom(t *testing.T) {
	store, _ := newTestConfirmations()
	first, _, _ := store.issue(userOne, "delete_app", target)
	second, _, _ := store.issue(userOne, "delete_app", target)
	if first == second {
		t.Fatal("two tokens are the same")
	}
	if len(first) < 40 {
		t.Fatalf("token %q is too short to be unguessable", first)
	}
}

func TestConfirmationsExpireWithinTheWindow(t *testing.T) {
	store, clock := newTestConfirmations()
	token, expires, err := store.issue(userOne, "delete_app", target)
	if err != nil {
		t.Fatal(err)
	}
	if got := expires.Sub(clock.now); got != confirmTTL || confirmTTL > 5*time.Minute {
		t.Fatalf("expires in %v, want %v (at most five minutes)", got, confirmTTL)
	}
	clock.now = clock.now.Add(confirmTTL + time.Second)
	if err := store.redeem(userOne, token, "delete_app", target); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

func TestConfirmationsWorkUpToTheLastSecond(t *testing.T) {
	store, clock := newTestConfirmations()
	token, _, _ := store.issue(userOne, "delete_app", target)
	clock.now = clock.now.Add(confirmTTL - time.Second)
	if err := store.redeem(userOne, token, "delete_app", target); err != nil {
		t.Fatalf("a token inside its window was refused: %v", err)
	}
}

func TestConfirmationsAreBoundToTheirTarget(t *testing.T) {
	for name, other := range map[string]confirmTarget{
		"another app":     {project: "proj-a", id: "api"},
		"another project": {project: "proj-b", id: "web"},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestConfirmations()
			token, _, _ := store.issue(userOne, "delete_app", target)
			if err := store.redeem(userOne, token, "delete_app", other); err == nil {
				t.Fatal("a token was accepted for a target it was not issued for")
			}
			if err := store.redeem(userOne, token, "delete_app", target); err == nil {
				t.Fatal("a token refused for the wrong target stayed usable")
			}
		})
	}
}

func TestConfirmationsAreBoundToTheirTool(t *testing.T) {
	store, _ := newTestConfirmations()
	token, _, _ := store.issue(userOne, "delete_app", target)
	if err := store.redeem(userOne, token, "delete_function", target); err == nil {
		t.Fatal("a delete_app token confirmed delete_function")
	}
}

func TestConfirmationsAreBoundToTheirCaller(t *testing.T) {
	sameUserOtherToken := Caller{User: userNamed("user-1"), Token: tokenNamed("hash-9")}
	otherUserSameToken := Caller{User: userNamed("user-2"), Token: tokenNamed("hash-1")}
	for name, caller := range map[string]Caller{
		"another user": userTwo, "another token of the same user": sameUserOtherToken, "another user on the same token": otherUserSameToken,
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestConfirmations()
			token, _, _ := store.issue(userOne, "delete_app", target)
			if err := store.redeem(caller, token, "delete_app", target); err == nil {
				t.Fatal("a token was accepted from a caller it was not issued to")
			}
			if err := store.redeem(userOne, token, "delete_app", target); err != nil {
				t.Fatalf("a stranger's attempt used up the owner's token: %v", err)
			}
		})
	}
}

func TestConfirmationsRefuseGuessesAndEmptyTokens(t *testing.T) {
	store, _ := newTestConfirmations()
	if _, _, err := store.issue(userOne, "delete_app", target); err != nil {
		t.Fatal(err)
	}
	for _, guess := range []string{"", " ", "mcpc_", "not-a-token", "mcpc_" + strings.Repeat("A", 43)} {
		if err := store.redeem(userOne, guess, "delete_app", target); err == nil {
			t.Errorf("%q was accepted", guess)
		}
	}
}

func TestConfirmationsRedeemOnceUnderRace(t *testing.T) {
	store, _ := newTestConfirmations()
	token, _, _ := store.issue(userOne, "delete_app", target)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if store.redeem(userOne, token, "delete_app", target) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("%d redeems succeeded, want exactly 1", accepted.Load())
	}
}

func TestConfirmationsAreBounded(t *testing.T) {
	store, clock := newTestConfirmations()
	for i := 0; i < maxPendingConfirmations; i++ {
		if _, _, err := store.issue(userOne, "delete_app", target); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, _, err := store.issue(userOne, "delete_app", target); err == nil {
		t.Fatal("the store grew past its bound")
	}
	clock.now = clock.now.Add(confirmTTL + time.Second)
	if _, _, err := store.issue(userOne, "delete_app", target); err != nil {
		t.Fatalf("expired tokens were not given up: %v", err)
	}
}

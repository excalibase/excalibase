package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type fakePersonalOrgs struct {
	asked     []*domain.Org
	takenUpTo int // the first takenUpTo attempts find their slug taken
	err       error
}

func (f *fakePersonalOrgs) EnsurePersonalOrg(_ context.Context, org *domain.Org) (bool, error) {
	f.asked = append(f.asked, org)
	if f.err != nil {
		return false, f.err
	}
	if len(f.asked) <= f.takenUpTo {
		return false, storage.ErrOrgSlugTaken
	}
	return true, nil
}

func TestEnsurePersonalOrgAsksForAFreeOrgOwnedByTheUser(t *testing.T) {
	store := &fakePersonalOrgs{}
	user := &domain.User{ID: "u-1", Username: "Ada.Lovelace"}

	if err := EnsurePersonalOrg(context.Background(), store, user); err != nil {
		t.Fatalf("EnsurePersonalOrg: %v", err)
	}
	if len(store.asked) != 1 {
		t.Fatalf("asked %d times", len(store.asked))
	}
	org := store.asked[0]
	if org.OwnerID != "u-1" || org.Tier != domain.Free || org.ID == "" {
		t.Fatalf("org = %+v", org)
	}
	if org.Name != "Ada.Lovelace's organization" {
		t.Errorf("name = %q", org.Name)
	}
	if org.Slug != "ada-lovelace" {
		t.Errorf("slug = %q", org.Slug)
	}
}

// A taken slug is retried with a random suffix, never given up on silently.
func TestEnsurePersonalOrgRetriesATakenSlug(t *testing.T) {
	store := &fakePersonalOrgs{takenUpTo: 2}
	if err := EnsurePersonalOrg(context.Background(), store, &domain.User{ID: "u-1", Username: "ada"}); err != nil {
		t.Fatalf("EnsurePersonalOrg: %v", err)
	}
	if len(store.asked) != 3 {
		t.Fatalf("asked %d times, want 3", len(store.asked))
	}
	retried := store.asked[2].Slug
	if !strings.HasPrefix(retried, "ada-") || retried == "ada" || !validTestSlug(retried) {
		t.Fatalf("retry slug = %q", retried)
	}
}

func TestEnsurePersonalOrgFailsWhenEverySlugIsTaken(t *testing.T) {
	store := &fakePersonalOrgs{takenUpTo: 100}
	err := EnsurePersonalOrg(context.Background(), store, &domain.User{ID: "u-1", Username: "ada"})
	if !errors.Is(err, storage.ErrOrgSlugTaken) {
		t.Fatalf("got %v, want ErrOrgSlugTaken", err)
	}
}

func TestEnsurePersonalOrgReturnsStoreErrors(t *testing.T) {
	store := &fakePersonalOrgs{err: errors.New("db down")}
	if err := EnsurePersonalOrg(context.Background(), store, &domain.User{ID: "u-1", Username: "ada"}); err == nil {
		t.Fatal("a store error must be returned")
	}
	if len(store.asked) != 1 {
		t.Fatalf("a non-slug error must not be retried, asked %d times", len(store.asked))
	}
}

// Service principals never sign in to Studio and get no org.
func TestEnsurePersonalOrgSkipsServicePrincipals(t *testing.T) {
	store := &fakePersonalOrgs{}
	user := &domain.User{ID: "svc", Username: "svc-auth", Kind: domain.UserKindService}
	if err := EnsurePersonalOrg(context.Background(), store, user); err != nil {
		t.Fatalf("EnsurePersonalOrg: %v", err)
	}
	if len(store.asked) != 0 {
		t.Fatal("a service principal was given an org")
	}
}

func TestPersonalOrgNameAndSlugStayWithinTheRules(t *testing.T) {
	for _, username := range []string{
		"a", "__", "Ünïcödé", strings.Repeat("x", 200), "--a--", "name with spaces", "ab",
	} {
		name := PersonalOrgName(username)
		if _, ok := domain.NormalizeOrgName(name); !ok {
			t.Errorf("name for %q is outside the org name rules: %q (%d runes)", username, name, utf8.RuneCountInString(name))
		}
		for attempt := range 3 {
			if slug := personalOrgSlug(username, attempt); !validTestSlug(slug) {
				t.Errorf("slug for %q attempt %d is invalid: %q", username, attempt, slug)
			}
		}
	}
}

func validTestSlug(slug string) bool {
	if len(slug) < 2 || len(slug) > 50 {
		return false
	}
	for i, r := range slug {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !alnum && (r != '-' || i == 0) {
			return false
		}
	}
	return true
}

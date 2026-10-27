//go:build integration

package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Exercises migration 000086: what a sign-in mail says about its project and
// the per-project daily budget the relay draws on.

func TestSignInMailProject_ReadsNameSiteAndSwitch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "proj_mail_a", ProjectName: "Acme Notes", OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SignInMailProject(ctx, "proj_mail_a"); err != nil || got != (domain.SignInMailProject{Exists: true, Name: "Acme Notes"}) {
		t.Fatalf("no settings yet: %+v %v", got, err)
	}
	signIn := domain.DefaultAuthSignIn()
	signIn.EmailOTPEnabled = true
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_mail_a", domain.ProjectAuthSettings{SiteURL: "https://notes.acme.test"}, signIn); err != nil {
		t.Fatal(err)
	}
	want := domain.SignInMailProject{Exists: true, Name: "Acme Notes", SiteURL: "https://notes.acme.test", EmailOTPEnabled: true}
	if got, err := store.SignInMailProject(ctx, "proj_mail_a"); err != nil || got != want {
		t.Fatalf("got %+v %v, want %+v", got, err, want)
	}
	if got, err := store.SignInMailProject(ctx, "proj_mail_ghost"); err != nil || got != (domain.SignInMailProject{}) {
		t.Fatalf("an unknown project: %+v %v", got, err)
	}
}

// EXC-662: the added-sign-in-method notice goes out with only Google/GitHub on.
func TestSignInMailProject_ReadsWhetherAnyProviderIsOn(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "proj_mail_oauth", ProjectName: "Acme Notes", OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		provider domain.ProjectAuthProvider
		want     bool
	}{
		{domain.ProjectAuthProvider{Provider: "google", Enabled: false, ClientID: "g"}, false},
		{domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "h"}, true},
		{domain.ProjectAuthProvider{Provider: "github", Enabled: false, ClientID: "h"}, false},
	}
	for _, step := range steps {
		if err := store.PutAuthProvider(ctx, "proj_mail_oauth", step.provider); err != nil {
			t.Fatal(err)
		}
		got, err := store.SignInMailProject(ctx, "proj_mail_oauth")
		if err != nil || !got.Exists || got.OAuthEnabled != step.want {
			t.Fatalf("after %+v: %+v %v", step.provider, got, err)
		}
	}
}

func TestSignInMailBudget_HoldsPerProjectUnderConcurrentSends(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var taken atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.TakeSignInMail(ctx, "proj_mail_budget", 3)
			if err != nil {
				t.Errorf("take: %v", err)
			}
			if ok {
				taken.Add(1)
			}
		}()
	}
	wg.Wait()
	if taken.Load() != 3 {
		t.Fatalf("took %d of 8 with a budget of 3", taken.Load())
	}
	if ok, err := store.TakeSignInMail(ctx, "proj_mail_other", 3); err != nil || !ok {
		t.Fatalf("another project shares the budget: %v %v", ok, err)
	}
}

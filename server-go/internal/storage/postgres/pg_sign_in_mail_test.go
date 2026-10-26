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
	if got, err := store.SignInMailProject(ctx, "proj_mail_a"); err != nil || got != (domain.SignInMailProject{Name: "Acme Notes"}) {
		t.Fatalf("no settings yet: %+v %v", got, err)
	}
	signIn := domain.DefaultAuthSignIn()
	signIn.EmailOTPEnabled = true
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_mail_a", domain.ProjectAuthSettings{SiteURL: "https://notes.acme.test"}, signIn); err != nil {
		t.Fatal(err)
	}
	want := domain.SignInMailProject{Name: "Acme Notes", SiteURL: "https://notes.acme.test", EmailOTPEnabled: true}
	if got, err := store.SignInMailProject(ctx, "proj_mail_a"); err != nil || got != want {
		t.Fatalf("got %+v %v, want %+v", got, err, want)
	}
	if got, err := store.SignInMailProject(ctx, "proj_mail_ghost"); err != nil || got.EmailOTPEnabled {
		t.Fatalf("an unknown project: %+v %v", got, err)
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

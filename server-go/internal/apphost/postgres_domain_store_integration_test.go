//go:build integration

package apphost_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func domainFor(app *apphost.App, host string) *apphost.Domain {
	return &apphost.Domain{ID: app.ID + "-" + host, ProjectID: app.ProjectID, AppID: app.ID, Hostname: host,
		Status: apphost.DomainPending, CreatedAt: time.Now().UTC()}
}

func TestPGDomainStore_AddListGetDelete(t *testing.T) {
	_, app := createdApp(t, "proj_dom_rt", "app_dom_rt")
	s := apphost.NewPostgresDomainStore(sharedDB)
	d := domainFor(app, "shop.example.com")
	if err := s.Add(d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(domainFor(app, "shop.example.com")); !errors.Is(err, apphost.ErrDomainExists) {
		t.Fatalf("duplicate: %v", err)
	}
	list, err := s.List(app.ProjectID, app.ID)
	if err != nil || len(list) != 1 || list[0].Hostname != "shop.example.com" {
		t.Fatalf("List = %v, %v", list, err)
	}
	if got, _ := s.Get("proj_other", app.ID, d.ID); got != nil {
		t.Fatal("another project must not see the domain")
	}
	if err := s.Delete("proj_other", app.ID, d.ID); !errors.Is(err, apphost.ErrDomainNotFound) {
		t.Fatalf("cross-project delete: %v", err)
	}
	if err := s.Delete(app.ProjectID, app.ID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := s.Get(app.ProjectID, app.ID, d.ID); got != nil {
		t.Fatal("still there")
	}
}

func TestPGDomainStore_AtMostFivePerApp(t *testing.T) {
	_, app := createdApp(t, "proj_dom_cap", "app_dom_cap")
	s := apphost.NewPostgresDomainStore(sharedDB)
	for i := 0; i < apphost.MaxDomainsPerApp; i++ {
		if err := s.Add(domainFor(app, fmt.Sprintf("d%d.example.com", i))); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if err := s.Add(domainFor(app, "one-too-many.example.com")); !errors.Is(err, apphost.ErrDomainLimit) {
		t.Fatalf("sixth: %v", err)
	}
}

// Anyone may claim a hostname; only the app that proves it holds it.
func TestPGDomainStore_OnlyAVerifiedDomainHoldsTheHostname(t *testing.T) {
	_, owner := createdApp(t, "proj_dom_owner", "app_dom_owner")
	_, squatter := createdApp(t, "proj_dom_squat", "app_dom_squat")
	s := apphost.NewPostgresDomainStore(sharedDB)
	squat := domainFor(squatter, "www.victim.example")
	if err := s.Add(squat); err != nil {
		t.Fatal(err)
	}
	real := domainFor(owner, "www.victim.example")
	if err := s.Add(real); err != nil {
		t.Fatalf("a pending claim must not block the owner: %v", err)
	}
	now := time.Now().UTC()
	if err := s.Verify(owner.ProjectID, owner.ID, real.ID, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := s.Verify(squatter.ProjectID, squatter.ID, squat.ID, now); !errors.Is(err, apphost.ErrDomainClaimed) {
		t.Fatalf("second verify: %v", err)
	}
	got, _ := s.Get(owner.ProjectID, owner.ID, real.ID)
	if got.Status != apphost.DomainIssuing || got.VerifiedAt == nil || got.ConsecutiveFailures != 0 {
		t.Fatalf("verified = %+v", got)
	}
	routable, err := s.ListRoutable()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range routable {
		if d.ID == squat.ID {
			t.Fatal("a pending domain is not routable")
		}
		found = found || d.ID == real.ID
	}
	if !found {
		t.Fatal("the verified domain is routable")
	}

	if err := s.SetStatus(real.ID, apphost.DomainDetached, "cname moved", 3, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(squatter.ProjectID, squatter.ID, squat.ID, now); err != nil {
		t.Fatalf("once detached the hostname is free: %v", err)
	}
	if err := s.SetStatus("nope", apphost.DomainActive, "", 0, now); !errors.Is(err, apphost.ErrDomainNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := s.SetStatus(real.ID, "bogus", "", 0, now); err == nil {
		t.Fatal("an unknown status must be refused")
	}
}

func TestPGDomainStore_GoesWithItsApp(t *testing.T) {
	s, app := createdApp(t, "proj_dom_cascade", "app_dom_cascade")
	domains := apphost.NewPostgresDomainStore(sharedDB)
	if err := domains.Add(domainFor(app, "shop.cascade.example")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeProjectApps(app.ProjectID); err != nil {
		t.Fatal(err)
	}
	if list, _ := domains.List(app.ProjectID, app.ID); len(list) != 0 {
		t.Fatal("the domain outlived its app")
	}
}

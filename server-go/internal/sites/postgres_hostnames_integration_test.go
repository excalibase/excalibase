//go:build integration

package sites_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// Runs the hostname store against a real platform database, which also proves migration 000078 applies.

func newHostname(id, site, project, host string) *sites.CustomHostname {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &sites.CustomHostname{ID: id, SiteID: site, ProjectID: project, Hostname: host, Validation: "http",
		Status: sites.HostnamePending, Records: []sites.DNSInstruction{{Type: "CNAME", Name: host, Value: "cname.example.app", Purpose: "routing"}},
		Errors: []string{}, CreatedBy: "dev", CreatedAt: now, UpdatedAt: now, VerificationToken: "tok-" + id, InactiveSince: &now}
}

// Only a proven row holds a name across projects; a project claims a name once.
func TestPostgresOnlyAProvenClaimHoldsAHostname(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("hn-pr-a", "proj-hn-pr-a", "hn-pr-a"))
	seedSite(t, store, newSite("hn-pr-b", "proj-hn-pr-b", "hn-pr-b"))
	seedSite(t, store, newSite("hn-pr-c", "proj-hn-pr-c", "hn-pr-c"))
	squatter, owner := newHostname("hn-pr-1", "hn-pr-a", "proj-hn-pr-a", "www.hn-pr.com"), newHostname("hn-pr-2", "hn-pr-b", "proj-hn-pr-b", "www.hn-pr.com")
	for _, claim := range []*sites.CustomHostname{squatter, owner} {
		if err := store.CreateHostname(ctx, claim, 2); err != nil {
			t.Fatalf("an unproven claim refused another: %v", err)
		}
	}
	if err := store.CreateHostname(ctx, newHostname("hn-pr-3", "hn-pr-a", "proj-hn-pr-a", "www.hn-pr.com"), 2); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want one claim per project", err)
	}
	proven := time.Now().UTC().Truncate(time.Microsecond)
	owner.VerifiedAt = &proven
	if err := store.VerifyHostname(ctx, owner, 90); err != nil {
		t.Fatalf("verify: %v", err)
	}
	squatter.VerifiedAt = &proven
	if err := store.VerifyHostname(ctx, squatter, 90); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want the proven claim to hold the name", err)
	}
	if err := store.UpdateHostname(ctx, squatter); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want an update unable to prove it either", err)
	}
	if err := store.CreateHostname(ctx, newHostname("hn-pr-4", "hn-pr-c", "proj-hn-pr-c", "www.hn-pr.com"), 2); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want a proven name refused to a new claim", err)
	}
}

// The zone's cap counts proven rows across every project.
func TestPostgresVerifyHostnameHoldsTheZoneCap(t *testing.T) {
	db := platformDB(t).DB()
	store := sites.NewPostgresStore(db)
	ctx := context.Background()
	var held int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM site_hostnames WHERE verified_at IS NOT NULL`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	seedSite(t, store, newSite("hn-cap", "proj-hn-cap", "hn-cap"))
	first, second := newHostname("hn-cap-1", "hn-cap", "proj-hn-cap", "a.hn-cap.com"), newHostname("hn-cap-2", "hn-cap", "proj-hn-cap", "b.hn-cap.com")
	proven := time.Now().UTC()
	for _, claim := range []*sites.CustomHostname{first, second} {
		claim.VerifiedAt = &proven
		if err := store.CreateHostname(ctx, claim, 5); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := store.VerifyHostname(ctx, first, held+1); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := store.VerifyHostname(ctx, second, held+1); !errors.Is(err, sites.ErrCustomDomainsFull) {
		t.Fatalf("err = %v, want the zone full", err)
	}
	if count, err := store.CountVerifiedHostnames(ctx); err != nil || count != held+1 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

func TestPostgresTheProjectLimitCountsEveryClaim(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("hn-site-a", "proj-hn-a", "hn-shop"))
	seedSite(t, store, newSite("hn-site-a2", "proj-hn-a", "hn-blog"))
	if err := store.CreateHostname(ctx, newHostname("hn-1", "hn-site-a", "proj-hn-a", "www.hn-example.com"), 2); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.CreateHostname(ctx, newHostname("hn-3", "hn-site-a2", "proj-hn-a", "blog.hn-example.com"), 2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if err := store.CreateHostname(ctx, newHostname("hn-4", "hn-site-a", "proj-hn-a", "news.hn-example.com"), 2); !errors.Is(err, sites.ErrHostnameLimitReached) {
		t.Fatalf("err = %v, want the limit", err)
	}
}

func TestPostgresHostnameRoundTripsItsStateAndIsScopedBySite(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("hn-rt-site", "proj-hn-rt", "hn-rt-shop"))
	created := newHostname("hn-rt-1", "hn-rt-site", "proj-hn-rt", "www.hn-rt.com")
	if err := store.CreateHostname(ctx, created, 2); err != nil {
		t.Fatalf("create: %v", err)
	}
	if stored, _ := store.GetHostname(ctx, "proj-hn-rt", "hn-rt-site", "hn-rt-1"); stored.VerificationToken != "tok-hn-rt-1" || stored.InactiveSince == nil {
		t.Fatalf("stored = %+v", stored)
	}
	activated := time.Now().UTC().Truncate(time.Microsecond)
	updated := *created
	updated.CloudflareID, updated.Status = "0d89c70d-ad9f-4843-b99f-6cc0252067e9", sites.HostnameActive
	updated.HostnameStatus, updated.CertificateStatus = "active", "active"
	updated.Errors, updated.CheckedAt, updated.VerifiedAt, updated.InactiveSince = []string{"none"}, activated, &activated, nil
	updated.WithdrawnAt = &activated
	if err := store.UpdateHostname(ctx, &updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetHostname(ctx, "proj-hn-rt", "hn-rt-site", "hn-rt-1")
	if err != nil || got == nil {
		t.Fatalf("get = %v err = %v", got, err)
	}
	if got.CloudflareID != updated.CloudflareID || got.Status != sites.HostnameActive || got.VerifiedAt == nil || got.InactiveSince != nil ||
		got.WithdrawnAt == nil || !got.WithdrawnAt.Equal(activated) ||
		!got.CheckedAt.Equal(activated) || len(got.Records) != 1 || got.Records[0].Value != "cname.example.app" || got.Errors[0] != "none" {
		t.Fatalf("got = %+v", got)
	}
	if other, err := store.GetHostname(ctx, "proj-other", "hn-rt-site", "hn-rt-1"); err != nil || other != nil {
		t.Fatalf("another project read it: %v %v", other, err)
	}
	if err := store.DeleteHostname(ctx, "proj-other", "hn-rt-site", "hn-rt-1"); !errors.Is(err, sites.ErrHostnameNotFound) {
		t.Fatalf("another project deleted it: %v", err)
	}
	if err := store.DeleteHostname(ctx, "proj-hn-rt", "hn-rt-site", "hn-rt-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.UpdateHostname(ctx, &updated); !errors.Is(err, sites.ErrHostnameNotFound) {
		t.Fatalf("update after delete = %v", err)
	}
}

// A site cannot be deleted while it still holds a custom hostname: the zone would keep one nobody tracks.
func TestPostgresSiteDeletionNeedsItsHostnamesGoneFirst(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("hn-fk-site", "proj-hn-fk", "hn-fk-shop"))
	if err := store.CreateHostname(ctx, newHostname("hn-fk-1", "hn-fk-site", "proj-hn-fk", "www.hn-fk.com"), 2); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-hn-fk", "hn-fk-site", "lease", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Delete(ctx, "proj-hn-fk", "hn-fk-site", "lease"); err == nil {
		t.Fatal("the site was deleted under its hostname")
	}
}

func TestPostgresDueHostnamesLeaveOutProjectsBeingDeletedAndRecentlyCheckedActiveOnes(t *testing.T) {
	db := platformDB(t).DB()
	store := sites.NewPostgresStore(db)
	ctx := context.Background()
	seedSite(t, store, newSite("hn-pend-live", "proj-hn-live", "hn-pend-live"))
	seedSite(t, store, newSite("hn-pend-gone", "proj-hn-gone", "hn-pend-gone"))
	for _, host := range []*sites.CustomHostname{
		newHostname("hn-pend-1", "hn-pend-live", "proj-hn-live", "www.hn-pend-live.com"),
		newHostname("hn-pend-2", "hn-pend-gone", "proj-hn-gone", "www.hn-pend-gone.com"),
	} {
		if err := store.CreateHostname(ctx, host, 2); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO database_instances
		(project_id, org_id, database_type, deployment_mode, status, storage_size, no_database)
		VALUES ('proj-hn-gone', 'org-hn', 'POSTGRESQL', 'k8s', 'PENDING_DELETION', '5Gi', true)`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	seedSite(t, store, newSite("hn-pend-act", "proj-hn-live", "hn-pend-act"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	for id, checked := range map[string]time.Time{"hn-pend-fresh": now, "hn-pend-stale": now.Add(-2 * time.Hour)} {
		active := newHostname(id, "hn-pend-act", "proj-hn-live", id+".example.com")
		if err := store.CreateHostname(ctx, active, 10); err != nil {
			t.Fatalf("create: %v", err)
		}
		active.Status, active.CheckedAt, active.InactiveSince = sites.HostnameActive, checked, nil
		if err := store.UpdateHostname(ctx, active); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	due, err := store.DueHostnames(ctx, now.Add(-time.Hour), 500)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	seen := map[string]bool{}
	for _, host := range due {
		seen[host.ID] = true
	}
	if !seen["hn-pend-1"] || seen["hn-pend-2"] || seen["hn-pend-fresh"] || !seen["hn-pend-stale"] {
		t.Fatalf("due = %v", seen)
	}
}

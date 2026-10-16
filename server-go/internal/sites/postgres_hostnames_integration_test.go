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

func TestPostgresHostnameIsGlobalAndTheProjectLimitHolds(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("hn-site-a", "proj-hn-a", "hn-shop"))
	seedSite(t, store, newSite("hn-site-a2", "proj-hn-a", "hn-blog"))
	seedSite(t, store, newSite("hn-site-b", "proj-hn-b", "hn-other"))
	if err := store.CreateHostname(ctx, newHostname("hn-1", "hn-site-a", "proj-hn-a", "www.hn-example.com"), 2); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.CreateHostname(ctx, newHostname("hn-2", "hn-site-b", "proj-hn-b", "www.hn-example.com"), 2); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want taken", err)
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
	if err := store.UpdateHostname(ctx, &updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetHostname(ctx, "proj-hn-rt", "hn-rt-site", "hn-rt-1")
	if err != nil || got == nil {
		t.Fatalf("get = %v err = %v", got, err)
	}
	if got.CloudflareID != updated.CloudflareID || got.Status != sites.HostnameActive || got.VerifiedAt == nil || got.InactiveSince != nil ||
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

func TestPostgresPendingHostnamesLeaveOutProjectsBeingDeleted(t *testing.T) {
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
	pending, err := store.PendingHostnames(ctx, 500)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	seen := map[string]bool{}
	for _, host := range pending {
		seen[host.ID] = true
	}
	if !seen["hn-pend-1"] || seen["hn-pend-2"] {
		t.Fatalf("pending = %v", seen)
	}
}

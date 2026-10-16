//go:build integration

package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func seedUser(t *testing.T, id string) {
	t.Helper()
	if err := platformDB(t).CreateUser(context.Background(), &domain.User{ID: id, Username: id, Email: id + "@example.test",
		PasswordHash: "x", Role: "user", Active: true}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func postgresService(t *testing.T) (*sites.Service, *sites.PostgresStore) {
	t.Helper()
	store := sites.NewPostgresStore(platformDB(t).DB())
	service, err := sites.NewService(store, acceptingPublisher{}, sitestest.NewOrigins(), sites.Options{BaseDomain: "example.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, store
}

// EXC-620: only the token's SHA-256 is kept, as for a personal access token,
// and revoking it ends it on the next lookup.
func TestPostgresDeployTokenIsKeptAsItsHash(t *testing.T) {
	service, store := postgresService(t)
	ctx := context.Background()
	seedUser(t, "token-u1")
	seedSite(t, store, newSite("token-1", "proj-token", "token-shop"))
	raw, token, err := service.CreateDeployToken(ctx, sites.DeployTokenRequest{ProjectID: "proj-token", SiteID: "token-1", Name: "ci", Actor: "token-u1"})
	if err != nil {
		t.Fatalf("CreateDeployToken: %v", err)
	}
	var row string
	if err := platformDB(t).DB().QueryRowContext(ctx, `SELECT concat_ws('|', id, site_id, project_id, name, token_prefix, token_hash, created_by)
		FROM site_deploy_tokens WHERE id = $1`, token.ID).Scan(&row); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if strings.Contains(row, raw) || !strings.Contains(row, auth.HashToken(raw)) {
		t.Fatalf("stored row %q keeps the token or lacks its hash", row)
	}
	resolved, err := service.ResolveDeployToken(ctx, raw)
	if err != nil || resolved == nil || resolved.SiteID != "token-1" || resolved.CreatedBy != "token-u1" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	deploy, err := service.Deploy(sites.WithDeployToken(ctx, resolved), sites.DeployRequest{ProjectID: "proj-token", SiteID: "token-1",
		Actor: "token-u1", Body: bytes.NewReader(sampleArchive(t))})
	if err != nil {
		t.Fatalf("deploy with the token: %v", err)
	}
	history, err := store.ListDeploys(ctx, "proj-token", "token-1", 10)
	if err != nil || len(history) != 1 || history[0].ID != deploy.ID || history[0].DeployTokenID != token.ID || history[0].DeployTokenName != "ci" {
		t.Fatalf("history = %+v, %v; want the deploy token recorded", history, err)
	}
	service.RecordDeployTokenUse(ctx, resolved)
	listed, err := store.ListDeployTokens(ctx, "proj-token", "token-1")
	if err != nil || len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("listed = %+v, %v; want its use recorded", listed, err)
	}
	if gap := listed[0].ExpiresAt.Sub(token.ExpiresAt); gap > time.Millisecond || gap < -time.Millisecond {
		t.Fatalf("stored expiry %v, made with %v", listed[0].ExpiresAt, token.ExpiresAt)
	}
	if err := store.DeleteDeployToken(ctx, "proj-other", "token-1", token.ID); !errors.Is(err, sites.ErrDeployTokenNotFound) {
		t.Fatalf("revoke through another project: %v", err)
	}
	if err := service.RevokeDeployToken(ctx, "proj-token", "token-1", token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if resolved, err := service.ResolveDeployToken(ctx, raw); err != nil || resolved != nil {
		t.Fatalf("a revoked token resolved to %+v, %v", resolved, err)
	}
}

func TestPostgresDeployTokenNeedsTheProjectsSiteAndHoldsTheCap(t *testing.T) {
	_, store := postgresService(t)
	ctx := context.Background()
	seedUser(t, "token-u2")
	seedSite(t, store, newSite("token-2", "proj-token-cap", "token-cap"))
	token := func(id, project string) *sites.DeployToken {
		return &sites.DeployToken{ID: id, SiteID: "token-2", ProjectID: project, Name: "ci", Prefix: "excd_x", Hash: "hash-" + id,
			CreatedBy: "token-u2", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	}
	if err := store.CreateDeployToken(ctx, token("cap-x", "proj-elsewhere"), 1); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("another project's site: %v", err)
	}
	if err := store.CreateDeployToken(ctx, token("cap-1", "proj-token-cap"), 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := store.CreateDeployToken(ctx, token("cap-2", "proj-token-cap"), 1); !errors.Is(err, sites.ErrDeployTokenLimitReached) {
		t.Fatalf("past the cap: %v", err)
	}
}

// Deleting the site, the project's sites, or the token's creator revokes it.
func TestPostgresDeployTokenGoesWithItsSiteAndItsCreator(t *testing.T) {
	service, store := postgresService(t)
	ctx := context.Background()
	seedUser(t, "token-u3")
	seedSite(t, store, newSite("token-3", "proj-token-del", "token-del"))
	seedSite(t, store, newSite("token-4", "proj-token-del", "token-del-two"))
	seedSite(t, store, newSite("token-5", "proj-token-user", "token-user"))
	rawSite, _, _ := service.CreateDeployToken(ctx, sites.DeployTokenRequest{ProjectID: "proj-token-del", SiteID: "token-3", Name: "ci", Actor: "token-u3"})
	rawProject, _, _ := service.CreateDeployToken(ctx, sites.DeployTokenRequest{ProjectID: "proj-token-del", SiteID: "token-4", Name: "ci", Actor: "token-u3"})
	if err := service.Delete(ctx, "proj-token-del", "token-3"); err != nil {
		t.Fatalf("delete site: %v", err)
	}
	if resolved, _ := service.ResolveDeployToken(ctx, rawSite); resolved != nil {
		t.Fatal("a deleted site's token still resolves")
	}
	if _, err := service.PurgeProjectSites(ctx, "proj-token-del"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if resolved, _ := service.ResolveDeployToken(ctx, rawProject); resolved != nil {
		t.Fatal("a deleted project's token still resolves")
	}
	rawUser, _, _ := service.CreateDeployToken(ctx, sites.DeployTokenRequest{ProjectID: "proj-token-user", SiteID: "token-5", Name: "ci", Actor: "token-u3"})
	if _, err := platformDB(t).DB().ExecContext(ctx, `DELETE FROM users WHERE id = 'token-u3'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if resolved, _ := service.ResolveDeployToken(ctx, rawUser); resolved != nil {
		t.Fatal("a deleted creator's token still resolves")
	}
}

// The deploy rate is the project's attempts in the last hour, taken atomically.
func TestPostgresTakeDeploySlotHoldsTheProjectsRate(t *testing.T) {
	_, store := postgresService(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 2 {
		wait, err := store.TakeDeploySlot(ctx, "proj-rate", base.Add(time.Duration(i)*10*time.Minute), time.Hour, 2)
		if err != nil || wait != 0 {
			t.Fatalf("attempt %d: wait %v err %v", i, wait, err)
		}
	}
	if wait, err := store.TakeDeploySlot(ctx, "proj-rate-other", base, time.Hour, 2); err != nil || wait != 0 {
		t.Fatalf("another project: wait %v err %v", wait, err)
	}
	wait, err := store.TakeDeploySlot(ctx, "proj-rate", base.Add(30*time.Minute), time.Hour, 2)
	if err != nil || wait != 30*time.Minute {
		t.Fatalf("third attempt: wait %v err %v, want 30m", wait, err)
	}
	if wait, err := store.TakeDeploySlot(ctx, "proj-rate", base.Add(61*time.Minute), time.Hour, 2); err != nil || wait != 0 {
		t.Fatalf("once the first left the hour: wait %v err %v", wait, err)
	}
	// Concurrent attempts never overshoot the rate.
	results := make(chan time.Duration, 10)
	for range 10 {
		go func() {
			wait, _ := store.TakeDeploySlot(ctx, "proj-rate-race", base, time.Hour, 3)
			results <- wait
		}()
	}
	taken := 0
	for range 10 {
		if <-results == 0 {
			taken++
		}
	}
	if taken != 3 {
		t.Fatalf("%d of 10 concurrent attempts got a slot, want 3", taken)
	}
}

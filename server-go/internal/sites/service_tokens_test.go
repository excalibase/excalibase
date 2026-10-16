package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// A deploy token (EXC-620) is shown once and kept only as its hash, like a
// personal access token, and resolves to the one site it was made for.
func TestDeployTokenIsShownOnceAndKeptAsItsHash(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	raw, token, err := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "github actions", testActor)
	if err != nil {
		t.Fatalf("CreateDeployToken: %v", err)
	}
	if !strings.HasPrefix(raw, sites.DeployTokenPrefix) || !sites.IsDeployToken(raw) || sites.IsDeployToken("excb_"+raw[5:]) {
		t.Fatalf("raw token %q does not carry the deploy-token prefix", raw)
	}
	if token.SiteID != site.ID || token.ProjectID != testProject || token.Name != "github actions" || token.CreatedBy != testActor || token.Prefix != auth.TokenPrefix(raw) {
		t.Fatalf("token = %+v", token)
	}
	stored := h.store.Tokens[token.ID]
	if stored.Hash != auth.HashToken(raw) || strings.Contains(stored.Hash, raw) {
		t.Fatalf("stored hash %q is not the token's SHA-256", stored.Hash)
	}
	listed, err := h.service.ListDeployTokens(context.Background(), testProject, site.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != token.ID {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	resolved, err := h.service.ResolveDeployToken(context.Background(), raw)
	if err != nil || resolved == nil || resolved.SiteID != site.ID || resolved.ProjectID != testProject {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
}

// Revoking takes effect at once: the next request with the token resolves to nothing.
func TestRevokedDeployTokenResolvesToNothing(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	raw, token, _ := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "ci", testActor)
	if err := h.service.RevokeDeployToken(context.Background(), testProject, site.ID, token.ID); err != nil {
		t.Fatalf("RevokeDeployToken: %v", err)
	}
	if resolved, err := h.service.ResolveDeployToken(context.Background(), raw); err != nil || resolved != nil {
		t.Fatalf("revoked token resolved to %+v, %v", resolved, err)
	}
	if err := h.service.RevokeDeployToken(context.Background(), testProject, site.ID, token.ID); !errors.Is(err, sites.ErrDeployTokenNotFound) {
		t.Fatalf("second revoke: err = %v", err)
	}
}

// Deleting the site, or the project's sites, revokes every token for them.
func TestDeletingTheSiteRevokesItsDeployTokens(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	raw, _, _ := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "ci", testActor)
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if resolved, _ := h.service.ResolveDeployToken(context.Background(), raw); resolved != nil {
		t.Fatal("a deleted site's token still resolves")
	}
	other := h.createSite(t, "other")
	raw, _, _ = h.service.CreateDeployToken(context.Background(), testProject, other.ID, "ci", testActor)
	if _, err := h.service.PurgeProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("PurgeProjectSites: %v", err)
	}
	if resolved, _ := h.service.ResolveDeployToken(context.Background(), raw); resolved != nil {
		t.Fatal("a deleted project's token still resolves")
	}
}

// A token is made for a site of the project in the path, never another's.
func TestDeployTokenNeedsTheProjectsOwnSite(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if _, _, err := h.service.CreateDeployToken(context.Background(), "proj-other", site.ID, "ci", testActor); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("another project's site: err = %v", err)
	}
	_, token, _ := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "ci", testActor)
	if err := h.service.RevokeDeployToken(context.Background(), "proj-other", site.ID, token.ID); !errors.Is(err, sites.ErrDeployTokenNotFound) {
		t.Fatalf("revoke through another project: err = %v", err)
	}
	if listed, _ := h.service.ListDeployTokens(context.Background(), "proj-other", site.ID); len(listed) != 0 {
		t.Fatalf("another project lists %d tokens", len(listed))
	}
}

func TestDeployTokenNameAndCountAreBounded(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	for _, name := range []string{"", "  ", strings.Repeat("x", 65), "line\nbreak", "ci\u202egol"} {
		var inputErr *sites.InputError
		if _, _, err := h.service.CreateDeployToken(context.Background(), testProject, site.ID, name, testActor); !errors.As(err, &inputErr) {
			t.Errorf("name %q: err = %v, want an input error", name, err)
		}
	}
	for i := range sites.MaxDeployTokensPerSite {
		if _, _, err := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "ci", testActor); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "ci", testActor); !errors.Is(err, sites.ErrDeployTokenLimitReached) {
		t.Fatalf("one past the cap: err = %v", err)
	}
}

// Anything that is not a deploy token is not looked up at all.
func TestResolveIgnoresOtherCredentials(t *testing.T) {
	h := newHarness(t)
	for _, raw := range []string{"", "excb_abc", sites.DeployTokenPrefix + "unknown"} {
		if resolved, err := h.service.ResolveDeployToken(context.Background(), raw); resolved != nil || err != nil {
			t.Errorf("%q resolved to %+v, %v", raw, resolved, err)
		}
	}
}

func TestDeployTokensNeedSites(t *testing.T) {
	service, err := sites.NewService(sitestest.NewMemoryStore(), nil, sitestest.NewOrigins(), sites.Options{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, _, err := service.CreateDeployToken(context.Background(), testProject, "s1", "ci", testActor); !errors.Is(err, sites.ErrSitesUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// A deploy made with a deploy token says which, so a leaked one can be traced.
func TestDeployRecordsTheDeployTokenThatMadeIt(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	_, token, _ := h.service.CreateDeployToken(context.Background(), testProject, site.ID, "github", testActor)
	deploy, err := h.service.Deploy(sites.WithDeployToken(context.Background(), token), sites.DeployRequest{
		ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t)),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if deploy.DeployTokenID != token.ID || deploy.DeployTokenName != "github" {
		t.Fatalf("deploy = %+v, want the token recorded", deploy)
	}
	stored := h.store.Deploys[deploy.ID]
	if stored.DeployTokenID != token.ID {
		t.Fatalf("stored deploy %+v lacks the token", stored)
	}
}

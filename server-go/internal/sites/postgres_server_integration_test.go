//go:build integration

package sites_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// Server sites (EXC-661) against the real platform database, which also proves migration 000090 applies.

type serverSitesHarness struct {
	service   *sites.Service
	store     *sites.PostgresStore
	publisher *sitestest.Publisher
	project   string
}

func newPostgresServerSites(t *testing.T, project string) *serverSitesHarness {
	t.Helper()
	store := sites.NewPostgresStore(platformDB(t).DB())
	publisher := sitestest.NewPublisher()
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), sites.Options{BaseDomain: "example.app", SpoolDir: t.TempDir(),
		Flags: features.NewStatic(features.SitesServer), Code: store, OutboundWorker: "excalibase-sites-outbound",
		Outbound: &sitestest.Outbound{Worker: "excalibase-sites-outbound"}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Each deploy a second after the last, so "newest" is never a tie.
	var mu sync.Mutex
	clock := time.Now().UTC().Truncate(time.Microsecond)
	service.SetNow(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	})
	return &serverSitesHarness{service: service, store: store, publisher: publisher, project: project}
}

func (h *serverSitesHarness) deploy(t *testing.T, site *sites.Site, worker string) (*sites.Deploy, error) {
	t.Helper()
	return h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: h.project, SiteID: site.ID, Actor: "dev",
		Body: bytes.NewReader(sitestest.ServerDist(t, worker))})
}

func (h *serverSitesHarness) keptCode(t *testing.T, siteID string) int {
	t.Helper()
	var kept int
	if err := platformDB(t).DB().QueryRow(`SELECT count(*) FROM site_deploy_code WHERE site_id = $1`, siteID).Scan(&kept); err != nil {
		t.Fatalf("count kept code: %v", err)
	}
	return kept
}

func TestPostgresServerSiteDeploysRollsBackAndKeepsItsCode(t *testing.T) {
	h := newPostgresServerSites(t, "proj-server-a")
	ctx := context.Background()
	site, err := h.service.CreateKind(ctx, h.project, "server-shop", sites.KindServer, nil, "dev")
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	first, err := h.deploy(t, site, "export default 'v1'")
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := h.deploy(t, site, "export default 'v2'"); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	read, err := h.store.GetDeploy(ctx, h.project, site.ID, first.ID)
	if err != nil || read.Kind != sites.KindServer || read.Server == nil || read.Server.Main != "worker.js" ||
		read.Server.CompatibilityDate != "2025-09-01" || read.Server.Modules != 2 {
		t.Fatalf("read = %+v err=%v", read, err)
	}
	if _, err := h.service.Rollback(ctx, h.project, site.ID, first.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if body := h.publisher.ModuleBodies[sites.ScriptName(site)]["worker.js"]; body != "export default 'v1'" {
		t.Fatalf("worker.js after rollback = %q", body)
	}
	if _, err := h.store.GetCode(ctx, "proj-other", site.ID, first.ID); !errors.Is(err, sites.ErrServerCodeGone) {
		t.Fatalf("another project read the code: err=%v", err)
	}
}

func TestPostgresServerCodeIsPrunedToTheWindowAndTheLiveDeploy(t *testing.T) {
	h := newPostgresServerSites(t, "proj-server-b")
	ctx := context.Background()
	site, err := h.service.CreateKind(ctx, h.project, "server-prune", sites.KindServer, nil, "dev")
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	oldest, err := h.deploy(t, site, "export default 0")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for i := range sites.ServerCodeKept {
		if _, err := h.deploy(t, site, "export default "+string(rune('a'+i))); err != nil {
			t.Fatalf("deploy %d: %v", i, err)
		}
	}
	if kept := h.keptCode(t, site.ID); kept != sites.ServerCodeKept {
		t.Fatalf("kept = %d, want %d", kept, sites.ServerCodeKept)
	}
	if _, err := h.service.Rollback(ctx, h.project, site.ID, oldest.ID); !errors.Is(err, sites.ErrServerCodeGone) {
		t.Fatalf("rollback past the window: err=%v", err)
	}
	// A failed publish keeps the live deploy and its code, and keeps none of its own.
	h.publisher.FailPut = errors.New("cloudflare answered 500")
	if _, err := h.deploy(t, site, "export default 'broken'"); err == nil {
		t.Fatal("want the publish to fail")
	}
	if kept := h.keptCode(t, site.ID); kept != sites.ServerCodeKept {
		t.Fatalf("kept after a failed deploy = %d, want %d", kept, sites.ServerCodeKept)
	}
	stored, _ := h.store.Get(ctx, h.project, site.ID)
	if _, err := h.store.GetCode(ctx, h.project, site.ID, stored.LiveDeployID); err != nil {
		t.Fatalf("the live deploy's code: %v", err)
	}
	h.publisher.FailPut = nil
	if err := h.service.Delete(ctx, h.project, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if kept := h.keptCode(t, site.ID); kept != 0 {
		t.Fatalf("kept after the site was deleted = %d", kept)
	}
}

func TestPostgresServerSitesCountAgainstTheirOwnLimit(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	server := newSite("kind-1", "proj-kind", "kind-ssr")
	server.Kind = sites.KindServer
	if err := store.Create(ctx, server, 5, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	second := newSite("kind-2", "proj-kind", "kind-ssr-two")
	second.Kind = sites.KindServer
	if err := store.Create(ctx, second, 5, 1); !errors.Is(err, sites.ErrServerSiteLimitReached) {
		t.Fatalf("err = %v, want the server site limit", err)
	}
	if err := store.Create(ctx, newSite("kind-3", "proj-kind", "kind-static"), 5, 1); err != nil {
		t.Fatalf("a static site beside it: %v", err)
	}
	read, err := store.Get(ctx, "proj-kind", "kind-3")
	if err != nil || read.Kind != sites.KindStatic {
		t.Fatalf("read = %+v err=%v", read, err)
	}
}

func TestPostgresServerCodeKeepsToTheSitesByteBudget(t *testing.T) {
	h := newPostgresServerSites(t, "proj-server-c")
	ctx := context.Background()
	site, err := h.service.CreateKind(ctx, h.project, "server-budget", sites.KindServer, nil, "dev")
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	var deploys []*sites.Deploy
	for i := range 3 {
		deploy, err := h.deploy(t, site, "export default "+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("deploy %d: %v", i, err)
		}
		deploys = append(deploys, deploy)
	}
	var newest int64
	if err := platformDB(t).DB().QueryRow(`SELECT octet_length(packed) FROM site_deploy_code WHERE deploy_id = $1`, deploys[2].ID).Scan(&newest); err != nil {
		t.Fatalf("size: %v", err)
	}
	// A budget that holds only the newest: the two before it go, the live one (the newest) stays.
	if err := h.store.PruneCode(ctx, h.project, site.ID, sites.CodeRetention{Keep: sites.ServerCodeKept, MaxBytes: newest, PendingSince: time.Now()}); err != nil {
		t.Fatalf("PruneCode: %v", err)
	}
	if kept := h.keptCode(t, site.ID); kept != 1 {
		t.Fatalf("kept = %d, want only the newest", kept)
	}
	listed, err := h.store.ListServerSites(ctx)
	if err != nil {
		t.Fatalf("ListServerSites: %v", err)
	}
	found := false
	for _, server := range listed {
		found = found || server.ID == site.ID
		if server.Kind != sites.KindServer {
			t.Fatalf("listed a %s site", server.Kind)
		}
	}
	if !found {
		t.Fatal("the server site is not listed")
	}
}

func TestPostgresServerCodeCloudflareRunsOutlivesLaterFailures(t *testing.T) {
	h := newPostgresServerSites(t, "proj-server-d")
	ctx := context.Background()
	site, err := h.service.CreateKind(ctx, h.project, "server-running", sites.KindServer, nil, "dev")
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	if _, err := h.deploy(t, site, "export default 'live'"); err != nil {
		t.Fatalf("first: %v", err)
	}
	h.publisher.FailKV = errors.New("kv down")
	if _, err := h.deploy(t, site, "export default 'running'"); err == nil {
		t.Fatal("want the route write to fail")
	}
	h.publisher.FailKV = nil
	h.publisher.FailSession = errors.New("cloudflare answered 500")
	if _, err := h.deploy(t, site, "export default 'never'"); err == nil {
		t.Fatal("want the upload session to fail")
	}
	var running string
	if err := platformDB(t).DB().QueryRow(`SELECT id FROM site_deploys WHERE site_id = $1 AND published_at IS NOT NULL
		ORDER BY published_at DESC LIMIT 1`, site.ID).Scan(&running); err != nil {
		t.Fatalf("published deploy: %v", err)
	}
	if _, err := h.store.GetCode(ctx, h.project, site.ID, running); err != nil {
		t.Fatalf("the code Cloudflare runs was pruned: %v", err)
	}
	// A pending deploy older than any lease keeps nothing.
	stale := &sites.Deploy{ID: "00000000-0000-4000-8000-0000000000d1", SiteID: site.ID, ProjectID: h.project, Kind: sites.KindServer,
		Status: sites.DeployPending, CreatedAt: time.Now().Add(-2 * sites.MaxDeployDuration)}
	if err := h.store.CreateDeploy(ctx, stale); err != nil {
		t.Fatalf("CreateDeploy: %v", err)
	}
	if err := h.store.PutCode(ctx, stale, []byte("x")); err != nil {
		t.Fatalf("PutCode: %v", err)
	}
	if err := h.store.PruneCode(ctx, h.project, site.ID, sites.CodeRetention{Keep: sites.ServerCodeKept, MaxBytes: 1 << 30,
		PendingSince: time.Now().Add(-sites.MaxDeployDuration)}); err != nil {
		t.Fatalf("PruneCode: %v", err)
	}
	if _, err := h.store.GetCode(ctx, h.project, site.ID, stale.ID); !errors.Is(err, sites.ErrServerCodeGone) {
		t.Fatalf("stale pending code: err=%v", err)
	}
}

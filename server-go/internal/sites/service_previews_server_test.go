package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// A server site's preview would publish its server code as public files, so it gets none (EXC-660).

const serverSecret = "export default { fetch() { return env.SECRET_KEY } }"

// serverDistWithIndex is a server build that also passes the static checks, so only the kind check stops it.
func serverDistWithIndex(t *testing.T) []byte {
	return sitestest.Gzip(t, sitestest.Tar(t,
		sitestest.Dir("./"),
		sitestest.File("./index.html", "<h1>ssr</h1>"),
		sitestest.File("./site.json", sitestest.ServerSiteJSON),
		sitestest.Dir("./server/"),
		sitestest.File("./server/worker.js", serverSecret),
	))
}

func newServerPreviewHarness(t *testing.T) *previewHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	previews := sitestest.NewMemoryPreviews(store)
	options := serverOptions(t, features.NewStatic(features.SitesServer), store)
	options.DeployLimits, options.Previews, options.MaxPreviews = previewLimits{max: 5}, previews, 100
	service, err := sites.NewService(store, publisher, origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &previewHarness{previews: previews, serviceHarness: &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}}
}

func wantStaticOnly(t *testing.T, err error) {
	t.Helper()
	var input *sites.InputError
	if !errors.As(err, &input) || !errors.Is(err, sites.ErrPreviewOfServerSite) || !strings.Contains(input.Reason, "static sites") {
		t.Fatalf("err = %v, want the static-sites-only InputError", err)
	}
}

func TestOpenPreviewRefusesAServerSite(t *testing.T) {
	h := newServerPreviewHarness(t)
	site := h.createServerSite(t, "ssr")
	_, err := h.open(t, site, sites.PreviewSourceBranch, "feat/a")
	wantStaticOnly(t, err)
	if len(h.previews.Previews) != 0 {
		t.Fatalf("a server site holds previews %+v", h.previews.Previews)
	}
}

func TestDeployPreviewNeverPublishesAServerSitesCode(t *testing.T) {
	h := newServerPreviewHarness(t)
	site := h.createServerSite(t, "ssr")
	// A preview row held from before the refusal, or written by hand: the deploy still refuses it.
	held := sites.Preview{ID: "pv-held", SiteID: site.ID, ProjectID: testProject, Source: sites.PreviewSourceBranch, Ref: "feat/a",
		Hostname: "ssr--held.example.app", Status: sites.PreviewBuilding}
	h.previews.Previews[held.ID] = held
	_, err := h.service.DeployPreview(context.Background(), sites.PreviewDeployRequest{ProjectID: testProject, SiteID: site.ID,
		PreviewID: held.ID, CommitSHA: "abc1234def", Body: bytes.NewReader(serverDistWithIndex(t))})
	wantStaticOnly(t, err)
	if len(h.publisher.Published) != 0 || len(h.publisher.Hosts) != 0 || h.publisher.Sessions != 0 {
		t.Fatalf("published %v, hosts %v, sessions %d; want nothing", h.publisher.Published, h.publisher.Hosts, h.publisher.Sessions)
	}
	for hash, body := range h.publisher.Stored {
		if strings.Contains(body, "SECRET_KEY") {
			t.Fatalf("server code uploaded as the public asset %s", hash)
		}
	}
}

func TestAServerSitesProductionDeployIsUnaffectedByARefusedPreview(t *testing.T) {
	h := newServerPreviewHarness(t)
	site := h.createServerSite(t, "ssr")
	_, err := h.open(t, site, sites.PreviewSourcePullRequest, "4")
	wantStaticOnly(t, err)
	deploy, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor,
		Body: bytes.NewReader(sitestest.ServerDist(t, serverSecret))})
	if err != nil || deploy.Status != sites.DeploySucceeded || deploy.Kind != sites.KindServer {
		t.Fatalf("production deploy = %+v, %v", deploy, err)
	}
	for hash, body := range h.publisher.Stored {
		if strings.Contains(body, "SECRET_KEY") {
			t.Fatalf("production published the server code as the public asset %s", hash)
		}
	}
}

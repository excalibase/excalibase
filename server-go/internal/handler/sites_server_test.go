package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func newServerSitesHarness(t *testing.T, on bool) *sitesHarness {
	t.Helper()
	publisher, origins, store := sitestest.NewPublisher(), sitestest.NewOrigins(), sitestest.NewMemoryStore()
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.SitesServer)
	}
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir(), Flags: flags, Code: store,
		OutboundWorker: "excalibase-sites-outbound"})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	handler.SetTokenStanding(fixedStanding{canDeploy: true})
	return &sitesHarness{router: sitesRouter(handler), publisher: publisher, origins: origins}
}

// Off, asking for a server site answers exactly as a route that is not mounted (EXC-554).
func TestSites_ServerKindAnswersAsUnmountedWhileOff(t *testing.T) {
	h := newServerSitesHarness(t, false)
	response := h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr","kind":"server"}`))
	if response.Code != http.StatusNotFound || response.Body.String() != "404 page not found\n" {
		t.Fatalf("create = %d %q", response.Code, response.Body)
	}
	if static := h.createSite(t, "plain"); static.Kind != sites.KindStatic {
		t.Fatalf("site = %+v", static)
	}
	if limits := h.do(t, http.MethodGet, "/limits", nil); strings.Contains(limits.Body.String(), "Server") {
		t.Fatalf("limits name server sites while they are off: %s", limits.Body)
	}
}

func TestSites_ServerSiteDeploysAndRollsBackWhileOn(t *testing.T) {
	h := newServerSitesHarness(t, true)
	response := h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr","kind":"server"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body)
	}
	var site sites.Site
	decodeSiteBody(t, response, &site)
	if site.Kind != sites.KindServer {
		t.Fatalf("site = %+v", site)
	}
	deployed := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.ServerDist(t, "export default 1"))
	if deployed.Code != http.StatusCreated || !strings.Contains(deployed.Body.String(), `"kind":"server"`) ||
		!strings.Contains(deployed.Body.String(), `"main":"worker.js"`) {
		t.Fatalf("deploy = %d %s", deployed.Code, deployed.Body)
	}
	var first struct{ ID string }
	decodeSiteBody(t, deployed, &first)
	if second := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.ServerDist(t, "export default 2")); second.Code != http.StatusCreated {
		t.Fatalf("second deploy = %d %s", second.Code, second.Body)
	}
	if back := h.do(t, http.MethodPost, "/"+site.ID+"/deploys/"+first.ID+"/rollback", nil); back.Code != http.StatusOK ||
		h.publisher.ModuleBodies[sites.ScriptName(&site)]["worker.js"] != "export default 1" {
		t.Fatalf("rollback = %d %s", back.Code, back.Body)
	}
	if again := h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr-two","kind":"server"}`)); again.Code != http.StatusConflict {
		t.Fatalf("second server site = %d %s, want the plan's limit", again.Code, again.Body)
	}
	if bad := h.do(t, http.MethodPost, "/", []byte(`{"name":"odd","kind":"edge"}`)); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d %s", bad.Code, bad.Body)
	}
}

func TestSites_LimitsNameTheServerSiteAllowance(t *testing.T) {
	h := newServerSitesHarness(t, true)
	response := h.do(t, http.MethodGet, "/limits", nil)
	if !strings.Contains(response.Body.String(), `"maxServerSites":1`) || !strings.Contains(response.Body.String(), `"maxServerBundleBytes":10485760`) ||
		!strings.Contains(response.Body.String(), `"maxServerCpuMs":50`) {
		t.Fatalf("limits = %s", response.Body)
	}
}

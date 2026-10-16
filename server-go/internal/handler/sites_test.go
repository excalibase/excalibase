package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
	"github.com/go-chi/chi/v5"
)

const sitesTestProject = "proj-sites1"

type sitesHarness struct {
	router    chi.Router
	publisher *sitestest.Publisher
	origins   *sitestest.Origins
}

func passThrough(next http.Handler) http.Handler { return next }

func newSitesHarness(t *testing.T) *sitesHarness {
	t.Helper()
	publisher, origins := sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	handler.SetTokenStanding(fixedStanding{canDeploy: true})
	return &sitesHarness{router: sitesRouter(handler), publisher: publisher, origins: origins}
}

func sitesRouter(h *SiteHandler) chi.Router {
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}/sites", func(r chi.Router) { h.Routes(r, passThrough) })
	return router
}

func (h *sitesHarness) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/projects/"+sitesTestProject+"/sites"+path, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h *sitesHarness) createSite(t *testing.T, name string) sites.Site {
	t.Helper()
	response := h.do(t, http.MethodPost, "/", []byte(`{"name":"`+name+`"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	var site sites.Site
	decodeSiteBody(t, response, &site)
	return site
}

func decodeSiteBody(t *testing.T, response *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
}

func TestSitesAnswerNotAvailableWithoutCloudflare(t *testing.T) {
	service, err := sites.NewService(sitestest.NewMemoryStore(), nil, sitestest.NewOrigins(), sites.Options{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	for _, handler := range []*SiteHandler{NewSiteHandler(service), NewSiteHandler(nil)} {
		h := &sitesHarness{router: sitesRouter(handler)}
		for _, call := range []struct{ method, path string }{
			{http.MethodGet, "/"}, {http.MethodPost, "/"}, {http.MethodGet, "/s1"}, {http.MethodPatch, "/s1"},
			{http.MethodDelete, "/s1"}, {http.MethodPost, "/s1/deploy"}, {http.MethodGet, "/s1/deploys"},
			{http.MethodGet, "/s1/deploys/d1"}, {http.MethodPost, "/s1/deploys/d1/rollback"}, {http.MethodGet, "/limits"},
			{http.MethodGet, "/s1/tokens"}, {http.MethodPost, "/s1/tokens"}, {http.MethodDelete, "/s1/tokens/t1"},
		} {
			response := h.do(t, call.method, call.path, []byte(`{}`))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not available on this install") {
				t.Errorf("%s %s = %d %s", call.method, call.path, response.Code, response.Body)
			}
		}
	}
}

func TestSiteCreateListGetUpdate(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	if site.Hostname != "shop.excalibase.app" || !site.SPAFallback {
		t.Fatalf("site = %+v", site)
	}
	var listed []sites.Site
	decodeSiteBody(t, h.do(t, http.MethodGet, "/", nil), &listed)
	if len(listed) != 1 || listed[0].ID != site.ID {
		t.Fatalf("listed = %+v", listed)
	}
	response := h.do(t, http.MethodPatch, "/"+site.ID, []byte(`{"spaFallback":false}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"spaFallback":false`) {
		t.Fatalf("patch = %d %s", response.Code, response.Body)
	}
	if got := h.do(t, http.MethodGet, "/"+site.ID, nil); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"url":"https://shop.excalibase.app"`) {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
}

func TestSiteRefusals(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	cases := []struct {
		name, method, path, body string
		status                   int
	}{
		{"bad name", http.MethodPost, "/", `{"name":"Not A Label"}`, http.StatusBadRequest},
		{"name taken", http.MethodPost, "/", `{"name":"shop"}`, http.StatusConflict},
		{"bad json", http.MethodPost, "/", `{`, http.StatusBadRequest},
		{"patch without setting", http.MethodPatch, "/" + site.ID, `{}`, http.StatusBadRequest},
		{"unknown site", http.MethodGet, "/missing", ``, http.StatusNotFound},
		{"bad site id", http.MethodGet, "/bad%20id", ``, http.StatusBadRequest},
		{"unknown deploy", http.MethodGet, "/" + site.ID + "/deploys/missing", ``, http.StatusNotFound},
		{"rollback unknown", http.MethodPost, "/" + site.ID + "/deploys/missing/rollback", ``, http.StatusNotFound},
		{"not an archive", http.MethodPost, "/" + site.ID + "/deploy", `plain text body that is not any kind of tar archive at all`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := h.do(t, tc.method, tc.path, []byte(tc.body))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body)
			}
		})
	}
}

func TestSiteDeployRefusesAnUnsafeArchive(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	archive := sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("../escape.js", "x"))
	response := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", archive)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "..") {
		t.Fatalf("deploy = %d %s", response.Code, response.Body)
	}
}

func TestSiteDeployLabelIsChecked(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	response := h.do(t, http.MethodPost, "/"+site.ID+"/deploy?label="+strings.Repeat("x", 201), sitestest.Dist(t, "x"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("deploy = %d %s", response.Code, response.Body)
	}
}

func TestSiteDeployFailureFromTheCDNIsABadGateway(t *testing.T) {
	h := newSitesHarness(t)
	h.publisher.FailPut = &cdnRefusal{}
	site := h.createSite(t, "shop")
	response := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.Dist(t, "x"))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("deploy = %d %s", response.Code, response.Body)
	}
}

type cdnRefusal struct{}

func (*cdnRefusal) Error() string { return "cloudflare answered 500: internal" }

// Drives the real handler with an archive the system tar wrote from a Vite-shaped dist folder.
func TestSiteDeployOfARealViteBuild(t *testing.T) {
	tarBinary, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("no tar binary on this host")
	}
	h := newSitesHarness(t)
	site := h.createSite(t, "clash-client")
	archive := filepath.Join(t.TempDir(), "dist.tgz")
	if output, err := exec.Command(tarBinary, "-czf", archive, "-C", filepath.Join("testdata", "vite-dist"), ".").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, output)
	}
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	response := h.do(t, http.MethodPost, "/"+site.ID+"/deploy?label=main@3f2a1bc", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("deploy = %d %s", response.Code, response.Body)
	}
	var deploy struct {
		sites.Deploy
		Live bool `json:"live"`
	}
	decodeSiteBody(t, response, &deploy)
	if deploy.Status != sites.DeploySucceeded || deploy.FileCount != 4 || !deploy.Live || deploy.Label != "main@3f2a1bc" {
		t.Fatalf("deploy = %+v", deploy)
	}
	if len(h.publisher.Stored) != 4 {
		t.Fatalf("uploaded %d files, want 4", len(h.publisher.Stored))
	}
	if strings.Join(h.origins.Origins, ",") != "https://clash-client.excalibase.app" {
		t.Fatalf("origins = %v", h.origins.Origins)
	}

	var history []map[string]any
	decodeSiteBody(t, h.do(t, http.MethodGet, "/"+site.ID+"/deploys", nil), &history)
	if len(history) != 1 || history[0]["live"] != true {
		t.Fatalf("history = %v", history)
	}
	if rollback := h.do(t, http.MethodPost, "/"+site.ID+"/deploys/"+deploy.ID+"/rollback", nil); rollback.Code != http.StatusOK {
		t.Fatalf("rollback = %d %s", rollback.Code, rollback.Body)
	}
	if deleted := h.do(t, http.MethodDelete, "/"+site.ID, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body)
	}
	if len(h.publisher.Deleted) != 1 || len(h.origins.Origins) != 0 {
		t.Fatalf("deleted=%v origins=%v", h.publisher.Deleted, h.origins.Origins)
	}
}

func TestSiteDeployOverTheBodyCeilingIsTooLarge(t *testing.T) {
	publisher, origins := sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{BaseDomain: "excalibase.app",
		SpoolDir: t.TempDir(), DeployLimits: tinyLimits{}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := &sitesHarness{router: sitesRouter(NewSiteHandler(service))}
	site := h.createSite(t, "shop")
	body := sitestest.Tar(t, sitestest.File("index.html", strings.Repeat("x", 64*1024)))
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+sitesTestProject+"/sites/"+site.ID+"/deploy", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("deploy = %d %s", recorder.Code, recorder.Body)
	}
}

type tinyLimits struct{}

func (tinyLimits) DeployLimits(context.Context, string) (sites.Limits, error) {
	return sites.Limits{MaxFiles: 2, MaxFileBytes: 1 << 10, MaxTotalBytes: 1 << 10, MaxSites: 1, MaxDeploysPerHour: 1}, nil
}

func TestSiteRoutesForAMissingSiteAreNotFound(t *testing.T) {
	h := newSitesHarness(t)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPatch, "/missing", `{"spaFallback":true}`},
		{http.MethodDelete, "/missing", ``},
		{http.MethodGet, "/missing/deploys", ``},
		{http.MethodGet, "/missing/deploys/d1", ``},
		{http.MethodPost, "/missing/deploy", ``},
	} {
		if response := h.do(t, call.method, call.path, []byte(call.body)); response.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s", call.method, call.path, response.Code, response.Body)
		}
	}
}

func TestSiteRoutesRefuseBadIDs(t *testing.T) {
	h := newSitesHarness(t)
	for _, call := range []struct{ method, path string }{
		{http.MethodPatch, "/bad%20id"}, {http.MethodDelete, "/bad%20id"}, {http.MethodPost, "/bad%20id/deploy"},
		{http.MethodGet, "/bad%20id/deploys"}, {http.MethodGet, "/s1/deploys/bad%20id"}, {http.MethodGet, "/bad%20id/deploys/d1"},
		{http.MethodPost, "/s1/deploys/bad%20id/rollback"}, {http.MethodPost, "/bad%20id/deploys/d1/rollback"},
	} {
		if response := h.do(t, call.method, call.path, []byte(`{}`)); response.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d %s", call.method, call.path, response.Code, response.Body)
		}
	}
	if response := h.do(t, http.MethodPatch, "/s1", []byte(`{`)); response.Code != http.StatusBadRequest {
		t.Errorf("bad json patch = %d", response.Code)
	}
}

func TestSiteGetDeployMarksTheLiveOne(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	created := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.Dist(t, "x"))
	var deploy sites.Deploy
	decodeSiteBody(t, created, &deploy)
	response := h.do(t, http.MethodGet, "/"+site.ID+"/deploys/"+deploy.ID, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"live":true`) {
		t.Fatalf("get deploy = %d %s", response.Code, response.Body)
	}
}

func TestSiteStoreFailureIsAnInternalErrorWithoutDetail(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSiteError(recorder, errors.New("pq: connection refused 10.0.0.1"))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "10.0.0.1") {
		t.Fatalf("answer = %d %s", recorder.Code, recorder.Body)
	}
	for err, status := range map[error]int{
		sites.ErrSitesUnavailable:                        http.StatusServiceUnavailable,
		sites.ErrSiteBusy:                                http.StatusConflict,
		sites.ErrRollbackNeedsFiles:                      http.StatusConflict,
		&sites.ArchiveError{Reason: "x", TooLarge: true}: http.StatusRequestEntityTooLarge,
		&http.MaxBytesError{Limit: 1}:                    http.StatusRequestEntityTooLarge,
		&sites.InputError{Reason: "bad"}:                 http.StatusBadRequest,
	} {
		recorder := httptest.NewRecorder()
		writeSiteError(recorder, err)
		if recorder.Code != status {
			t.Errorf("%v = %d, want %d", err, recorder.Code, status)
		}
	}
}

func TestSiteDeployFailureShowsNoAccountDetail(t *testing.T) {
	h := newSitesHarness(t)
	h.publisher.FailPut = errors.New("PUT /client/v4/accounts/acct-9f3e/workers/dispatch/namespaces/ns-prod-7/scripts/x: timeout")
	site := h.createSite(t, "shop")
	response := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.Dist(t, "x"))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "acct-9f3e") || strings.Contains(response.Body.String(), "ns-prod-7") {
		t.Fatalf("deploy = %d %s", response.Code, response.Body)
	}
	history := h.do(t, http.MethodGet, "/"+site.ID+"/deploys", nil).Body.String()
	if strings.Contains(history, "acct-9f3e") || strings.Contains(history, "ns-prod-7") {
		t.Fatalf("history shows account detail: %s", history)
	}
}

func TestSiteDeployAdmissionRefusalsAreTooManyRequests(t *testing.T) {
	for _, err := range []error{sites.ErrTooManyDeploys, sites.ErrProjectDeployRunning} {
		recorder := httptest.NewRecorder()
		writeSiteError(recorder, err)
		if recorder.Code != http.StatusTooManyRequests {
			t.Errorf("%v = %d, want 429", err, recorder.Code)
		}
	}
}

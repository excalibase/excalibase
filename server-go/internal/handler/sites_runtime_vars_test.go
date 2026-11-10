package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func TestSites_RuntimeVariablesRoundTripAndStayOutOfTheDeployListing(t *testing.T) {
	h := newServerSitesHarness(t, true)
	created := h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr","kind":"server"}`))
	var site sites.Site
	decodeSiteBody(t, created, &site)
	put := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(`{"vars":{"EXCALIBASE_PUBLISHABLE_KEY":"pk_1","MODE":"a"}}`))
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d %s", put.Code, put.Body)
	}
	got := h.do(t, http.MethodGet, "/"+site.ID+"/env", nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"MODE":"a"`) {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if deployed := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", sitestest.ServerDist(t, "export default 1")); deployed.Code != http.StatusCreated ||
		strings.Contains(deployed.Body.String(), "pk_1") {
		t.Fatalf("deploy = %d %s", deployed.Code, deployed.Body)
	}
	if listed := h.do(t, http.MethodGet, "/"+site.ID+"/deploys", nil); strings.Contains(listed.Body.String(), "pk_1") {
		t.Fatalf("the deploy listing carries the variables: %s", listed.Body)
	}
	if bad := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(`{"vars":{"EXCALIBASE_URL":"x"}}`)); bad.Code != http.StatusBadRequest {
		t.Fatalf("reserved name = %d %s", bad.Code, bad.Body)
	}
	if bad := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(`{}`)); bad.Code != http.StatusBadRequest {
		t.Fatalf("no vars = %d %s", bad.Code, bad.Body)
	}
}

func TestSites_RuntimeVariablesAnswerAsUnmountedWhileOff(t *testing.T) {
	h := newServerSitesHarness(t, false)
	response := h.do(t, http.MethodGet, "/some-site/env", nil)
	if response.Code != http.StatusNotFound || response.Body.String() != "404 page not found\n" {
		t.Fatalf("get = %d %q", response.Code, response.Body)
	}
}

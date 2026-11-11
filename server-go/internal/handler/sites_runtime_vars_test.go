package handler

import (
	"encoding/json"
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

func TestSites_RuntimeVariableBodyCapFollowsTheLimits(t *testing.T) {
	h := newServerSitesHarness(t, true)
	var site sites.Site
	decodeSiteBody(t, h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr","kind":"server"}`)), &site)
	big := `{"vars":{"CERT":"` + strings.Repeat("x", 5*1024) + `"}}`
	if put := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(big)); put.Code != http.StatusOK {
		t.Fatalf("a 5 KB value = %d %s, want the documented limit reachable", put.Code, put.Body)
	}
	over := `{"vars":{"CERT":"` + strings.Repeat("x", sites.MaxRuntimeVarsBodyBytes) + `"}}`
	if put := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(over)); put.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body past the cap = %d %s, want 413", put.Code, put.Body)
	}
	if put := h.do(t, http.MethodPut, "/"+site.ID+"/env", []byte(`{"vars":{"A":"`+strings.Repeat("x", 5*1024+1)+`"}}`)); put.Code != http.StatusBadRequest ||
		!strings.Contains(put.Body.String(), "larger than") {
		t.Fatalf("a value past 5 KB = %d %s, want the validator's reason", put.Code, put.Body)
	}
}

// The cap counts the bytes on the wire: a value of control characters is six bytes each once escaped.
func TestSites_RuntimeVariablesAtTheLimitsPassTheCapWhenHeavilyEscaped(t *testing.T) {
	h := newServerSitesHarness(t, true)
	var site sites.Site
	decodeSiteBody(t, h.do(t, http.MethodPost, "/", []byte(`{"name":"ssr","kind":"server"}`)), &site)
	vars := map[string]string{}
	for i := 0; i < 64; i++ {
		vars[strings.Repeat("N", 100)+strings.Repeat(string(rune('A'+i/26)), 1)+string(rune('a'+i%26))] = strings.Repeat("\x01", 5*1024)
	}
	body, err := json.Marshal(map[string]any{"vars": vars})
	if err != nil {
		t.Fatal(err)
	}
	if put := h.do(t, http.MethodPut, "/"+site.ID+"/env", body); put.Code != http.StatusOK {
		t.Fatalf("64 variables of 5 KB, escaped to %d bytes = %d %.200s", len(body), put.Code, put.Body)
	}
}

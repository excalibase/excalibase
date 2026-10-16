package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

type issuedDeployToken struct {
	sites.DeployToken
	Token string `json:"token"`
}

// A deploy token is shown once, on create; the list never carries it (EXC-620).
func TestSiteDeployTokensAreShownOnceListedAndRevoked(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	created := h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"github actions"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var issued issuedDeployToken
	decodeSiteBody(t, created, &issued)
	if !strings.HasPrefix(issued.Token, sites.DeployTokenPrefix) || issued.Name != "github actions" || issued.SiteID != site.ID || issued.ID == "" {
		t.Fatalf("issued = %+v", issued)
	}
	if strings.Contains(created.Body.String(), "hash") {
		t.Fatalf("create answer carries the hash: %s", created.Body)
	}

	listed := h.do(t, http.MethodGet, "/"+site.ID+"/tokens", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), issued.ID) || strings.Contains(listed.Body.String(), issued.Token) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body)
	}

	if revoked := h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil); revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", revoked.Code, revoked.Body)
	}
	if again := h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil); again.Code != http.StatusNotFound {
		t.Fatalf("second revoke = %d %s", again.Code, again.Body)
	}
}

func TestSiteDeployTokenRefusals(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	for name, call := range map[string]struct {
		method, path, body string
		want               int
	}{
		"bad json":     {http.MethodPost, "/" + site.ID + "/tokens", `{`, http.StatusBadRequest},
		"empty name":   {http.MethodPost, "/" + site.ID + "/tokens", `{"name":" "}`, http.StatusBadRequest},
		"missing site": {http.MethodPost, "/missing-site/tokens", `{"name":"ci"}`, http.StatusNotFound},
		"list missing": {http.MethodGet, "/missing-site/tokens", ``, http.StatusNotFound},
		"bad token id": {http.MethodDelete, "/" + site.ID + "/tokens/bad%20id", ``, http.StatusBadRequest},
	} {
		if got := h.do(t, call.method, call.path, []byte(call.body)); got.Code != call.want {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body, call.want)
		}
	}
	for range sites.MaxDeployTokensPerSite {
		h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`))
	}
	if over := h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`)); over.Code != http.StatusConflict {
		t.Fatalf("one past the cap = %d %s", over.Code, over.Body)
	}
}

type siteTokenAudit struct{ entries []*domain.AuditEntry }

func (a *siteTokenAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

// Making and revoking a deploy token is audited by prefix, never the token.
func TestSiteDeployTokensAreAudited(t *testing.T) {
	publisher, origins := sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	audit := &siteTokenAudit{}
	handler.SetAuditLog(audit)
	h := &sitesHarness{router: sitesRouter(handler)}
	site := h.createSite(t, "shop")
	var issued issuedDeployToken
	decodeSiteBody(t, h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`)), &issued)
	h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil)
	if len(audit.entries) != 2 || audit.entries[0].Action != "site_deploy_token.create" || audit.entries[1].Action != "site_deploy_token.revoke" {
		t.Fatalf("audit = %+v", audit.entries)
	}
	for _, entry := range audit.entries {
		if entry.ProjectID != sitesTestProject || entry.ResourceID != issued.ID || strings.Contains(entry.Details, issued.Token) || !strings.Contains(entry.Details, site.ID) {
			t.Errorf("entry = %+v", entry)
		}
	}
}

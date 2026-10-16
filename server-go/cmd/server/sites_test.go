package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func sitesConfig(zoneID string) config.AppConfig {
	return config.AppConfig{SitesEnabled: true, Sites: config.SitesConfig{
		AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "excalibase.app", HostsKVNamespaceID: "k", ZoneID: zoneID,
	}, AppDomain: "apps.example.test", PublicBaseURL: "https://api.example.test/v1", StudioURL: "https://studio.example.test"}
}

func TestSiteCustomDomainsNeedTheZone(t *testing.T) {
	if customDomainOptions(sitesConfig(""), fakestore.NewInstances()) != nil {
		t.Fatal("custom domains wired without a zone id")
	}
	options := customDomainOptions(sitesConfig("0123456789abcdef0123456789abcdef"), fakestore.NewInstances())
	if options == nil || options.Target != "cname.excalibase.app" || options.Zone == nil || options.Resolver == nil || options.Projects == nil {
		t.Fatalf("options = %+v", options)
	}
	for _, domain := range []string{"apps.example.test", "api.example.test", "studio.example.test"} {
		if !slices.Contains(options.ReservedDomains, domain) {
			t.Errorf("%s is not reserved: %v", domain, options.ReservedDomains)
		}
	}
}

func TestConfigReportsSiteCustomDomains(t *testing.T) {
	for zone, want := range map[string]bool{"": false, "0123456789abcdef0123456789abcdef": true} {
		recorder := httptest.NewRecorder()
		serveConfig(sitesConfig(zone), features.NewStatic())(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if strings.Contains(recorder.Body.String(), `"siteCustomDomains":true`) != want {
			t.Errorf("zone %q: %s", zone, recorder.Body)
		}
	}
}

func TestProjectGateStopsAProjectBeingDeleted(t *testing.T) {
	instances := fakestore.NewInstances()
	for project, status := range map[string]string{"live": "ACTIVE", "restoring": "RESTORING", "scheduled": "PENDING_DELETION", "tearing": "DELETING"} {
		instances.Items[project] = &domain.DatabaseInstance{ProjectID: project, Status: status}
	}
	gate := projectGate{instances: instances}
	for project, want := range map[string]bool{"live": true, "restoring": true, "scheduled": false, "tearing": false, "no-row": true} {
		serving, err := gate.ProjectServing(context.Background(), project)
		if err != nil || serving != want {
			t.Errorf("%s: serving=%v err=%v, want %v", project, serving, err, want)
		}
	}
}

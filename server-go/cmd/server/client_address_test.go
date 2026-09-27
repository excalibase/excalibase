package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/clientaddr"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// Behind the edge a client rotating a forged X-Forwarded-For per attempt must
// still share one login bucket: only the hop the edge appended is believed.
func TestLoginLimiterIgnoresForgedForwardedForBehindTheEdge(t *testing.T) {
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	deps.rlUnauth = custommw.RateLimit(custommw.PerIP, 3, time.Minute)
	edge, err := clientaddr.ParseTrustedProxies("10.42.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AppConfig{DeploymentMode: "cloud", TrustedProxyCIDRs: edge}
	router := buildRouter(cfg, platform, instances, deps)

	last := 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(`{"username":"nobody@example.test","password":"guess"}`))
		req.RemoteAddr = "10.42.0.9:40000"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d, 203.0.113.5", i))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("fifth login from one client answered %d; forged hops must not open new buckets", last)
	}
}

// Two real clients behind the same edge pod keep separate buckets.
func TestLoginLimiterSeparatesClientsBehindTheEdge(t *testing.T) {
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	deps.rlUnauth = custommw.RateLimit(custommw.PerIP, 1, time.Minute)
	edge, err := clientaddr.ParseTrustedProxies("10.42.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	router := buildRouter(config.AppConfig{DeploymentMode: "cloud", TrustedProxyCIDRs: edge}, platform, instances, deps)

	for _, client := range []string{"203.0.113.5", "203.0.113.6"} {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/setup-status", nil)
		req.RemoteAddr = "10.42.0.9:40000"
		req.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("client %s was refused on its first request: clients behind the edge share a bucket", client)
		}
	}
}

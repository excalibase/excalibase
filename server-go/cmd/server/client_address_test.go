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

// listenersWithLoginLimit builds both listeners over one router whose
// unauthenticated routes allow burst requests per client per minute.
func listenersWithLoginLimit(t *testing.T, burst int) (public, internal *http.Server) {
	t.Helper()
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	deps.rlUnauth = custommw.RateLimit(custommw.PerIP, burst, time.Minute)
	edge, err := clientaddr.ParseTrustedProxies("10.42.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AppConfig{DeploymentMode: "cloud", Port: "24005", PublicPort: "24006", TrustedProxyCIDRs: edge}
	return newListeners(cfg, buildRouter(cfg, platform, instances, deps))
}

func loginFrom(server *http.Server, peer, forwardedFor string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"nobody@example.test","password":"guess"}`))
	req.RemoteAddr = peer
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, req)
	return w.Code
}

func TestListenersBindThePublicAndInternalPorts(t *testing.T) {
	public, internal := listenersWithLoginLimit(t, 3)
	if public.Addr != ":24006" || internal.Addr != ":24005" {
		t.Fatalf("public %q, internal %q; want :24006 and :24005", public.Addr, internal.Addr)
	}
}

// Behind the edge a client rotating a forged X-Forwarded-For per attempt must
// still share one login bucket: only the hop the edge appended is believed.
func TestPublicListenerIgnoresForgedHopsBehindTheEdge(t *testing.T) {
	public, _ := listenersWithLoginLimit(t, 3)
	last := 0
	for i := 0; i < 5; i++ {
		last = loginFrom(public, "10.42.0.9:40000", fmt.Sprintf("192.0.2.%d, 203.0.113.5", i))
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("fifth login from one client answered %d; forged hops must not open new buckets", last)
	}
}

// Two real clients behind the same edge pod keep separate buckets.
func TestPublicListenerSeparatesClientsBehindTheEdge(t *testing.T) {
	public, _ := listenersWithLoginLimit(t, 1)
	for _, client := range []string{"203.0.113.5", "203.0.113.6"} {
		if code := loginFrom(public, "10.42.0.9:40000", client); code == http.StatusTooManyRequests {
			t.Fatalf("client %s was refused on its first request: clients behind the edge share a bucket", client)
		}
	}
}

// A pod inside the cluster (a tenant's function, say) reaches only the
// internal listener, which keys on the TCP peer whatever the header claims.
func TestInternalListenerIgnoresForwardedFor(t *testing.T) {
	_, internal := listenersWithLoginLimit(t, 3)
	last := 0
	for i := 0; i < 5; i++ {
		last = loginFrom(internal, "10.42.0.9:40000", fmt.Sprintf("203.0.113.%d", i))
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("fifth login from one pod answered %d; a forged X-Forwarded-For opened new buckets", last)
	}
	if code := loginFrom(internal, "10.42.0.10:40000", "203.0.113.1"); code == http.StatusTooManyRequests {
		t.Fatal("another pod was refused: the internal listener must key on the TCP peer")
	}
}

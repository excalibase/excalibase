package sites_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// The dispatch Worker finds a site's script by hostname in the hosts KV table.
func TestEveryDeployWritesTheHostnameAfterPublishing(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	for range 2 {
		if _, err := h.deploy(t, site, viteDist(t)); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	if h.publisher.Hosts["shop.excalibase.app"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
	calls := strings.Join(h.publisher.Calls, ",")
	if calls != "put-script,put-host,put-script,put-host" {
		t.Fatalf("calls = %s, want the host written after each publish", calls)
	}
}

func TestAFailedPublishWritesNoHostname(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailPut = errors.New("down")
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err == nil {
		t.Fatal("want the publish failure")
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
}

func TestAHostWriteFailureFailsTheDeploy(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailKV = errors.New("kv down")
	site := h.createSite(t, "shop")
	_, err := h.deploy(t, site, viteDist(t))
	if err == nil || !strings.Contains(err.Error(), "route the hostname") {
		t.Fatalf("err = %v", err)
	}
	stored, _ := h.store.Get(context.Background(), testProject, site.ID)
	if stored.LiveDeployID != "" {
		t.Fatalf("live = %q, want none while the hostname is not routed", stored.LiveDeployID)
	}
}

func TestRollbackWritesTheHostnameToo(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	delete(h.publisher.Hosts, site.Hostname)
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if h.publisher.Hosts[site.Hostname] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
}

// Traffic stops at the router first, then the Worker goes.
func TestDeleteRemovesTheHostnameBeforeTheScript(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.publisher.Calls = nil
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if strings.Join(h.publisher.Calls, ",") != "delete-host,delete-script" || len(h.publisher.Hosts) != 0 {
		t.Fatalf("calls = %v hosts = %v", h.publisher.Calls, h.publisher.Hosts)
	}
}

func TestPurgeRemovesEveryHostname(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"shop", "journal"} {
		site := h.createSite(t, name)
		if _, err := h.deploy(t, site, viteDist(t)); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	if _, err := h.service.PurgeProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
}

func TestAHostDeleteFailureKeepsTheSite(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	h.publisher.FailKV = errors.New("kv down")
	if err := h.service.Delete(context.Background(), testProject, site.ID); err == nil {
		t.Fatal("want the KV failure reported")
	}
	if stored, _ := h.store.Get(context.Background(), testProject, site.ID); stored == nil || len(h.publisher.Deleted) != 0 {
		t.Fatalf("site = %v, deleted scripts = %v", stored, h.publisher.Deleted)
	}
}

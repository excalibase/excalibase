package routepolicy

import (
	"strings"
	"testing"
)

// EXC-899: the trust of an org's owner decides what its apps, functions and
// hosts may do. Every route that changes something (any method but GET, HEAD or OPTIONS) has to
// say whether it resolves that trust, is exempt from it, or is unrelated, so a
// new route cannot be added without someone choosing. P4 makes "resolves" true
// in the handlers; until then the field is the checklist this test enforces.

func writeRows() []Row {
	var rows []Row
	for _, row := range Table() {
		for _, method := range row.Methods {
			if method != "GET" && method != "HEAD" && method != "OPTIONS" {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows
}

func TestEveryWriteRouteHasATrustClass(t *testing.T) {
	rows := writeRows()
	if len(rows) < 100 {
		t.Fatalf("found only %d write routes; the filter is wrong", len(rows))
	}
	for _, row := range rows {
		switch row.Trust {
		case trustResolves, trustExempt:
			if row.TrustWhy == "" {
				t.Errorf("%v %s is %q with no TrustWhy", row.Methods, row.Pattern, row.Trust)
			}
		case trustNone:
		default:
			t.Errorf("%v %s has no Trust class; set trustResolves, trustExempt or trustNone", row.Methods, row.Pattern)
		}
	}
}

// A route that touches apps, functions, sites or MCP may not call itself unrelated.
func TestTrustNoneIsNeverUsedOnAWorkloadRoute(t *testing.T) {
	workload := []string{"/apps", "app-templates", "app-network", "registry-credentials", "/projects/{projectId}/functions", "/functions/v1", "/sites", "site-builds", "/mcp",
		"log-drains", "auth-hooks", "db-endpoint", "s3-keys", "/internal/runtime/projects/{projectId}/scheduler"}
	for _, row := range writeRows() {
		if row.Trust != trustNone {
			continue
		}
		for _, part := range workload {
			if strings.Contains(row.Pattern, part) {
				t.Errorf("%v %s is marked trustNone but names %q", row.Methods, row.Pattern, part)
			}
		}
	}
}

// Resuming and unsuspending are never refused for trust (EXC-899 plan 15.10).
func TestResumeAndUnsuspendAreExempt(t *testing.T) {
	seen := 0
	for _, row := range writeRows() {
		if strings.HasSuffix(row.Pattern, "/resume") || strings.HasSuffix(row.Pattern, "/unsuspend") {
			seen++
			if row.Trust != trustExempt {
				t.Errorf("%s is %q; resume and unsuspend must be exempt", row.Pattern, row.Trust)
			}
		}
	}
	if seen < 3 {
		t.Errorf("found %d resume/unsuspend routes, want at least 3", seen)
	}
}

// Sites are exempt in v1; the reason must say what actually runs where, not only "Cloudflare".
func TestSitesAreExemptForAnAccurateReason(t *testing.T) {
	seen := 0
	for _, row := range writeRows() {
		if !strings.Contains(row.Pattern, "/sites") {
			continue
		}
		seen++
		if row.Trust != trustExempt {
			t.Errorf("%v %s is %q; sites are exempt in v1", row.Methods, row.Pattern, row.Trust)
		}
		for _, part := range []string{"builds", "gVisor", "Workers", "our Cloudflare account", "EXC-844"} {
			if !strings.Contains(row.TrustWhy, part) {
				t.Errorf("%v %s: the reason does not mention %q: %s", row.Methods, row.Pattern, part, row.TrustWhy)
			}
		}
	}
	if seen < 10 {
		t.Errorf("found %d site write routes, want at least 10", seen)
	}
}

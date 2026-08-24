package handler

import (
	"strings"
	"testing"
)

const testProjABC123 = "proj-abc123"


// TestBuildLogQL_KnownServices pins the LogQL selectors the admin /logs
// endpoint emits. If you change a service's app= label or namespace shape,
// update both the chart and this test.
func TestBuildLogQL_KnownServices(t *testing.T) {
	cases := []struct {
		svc       string
		projectID string
		search    string
		want      string
	}{
		{"auth", "", "", `{namespace="excalibase-platform",app="auth"}`},
		{"graphql", "", "", `{namespace="excalibase-platform",app="graphql"}`},
		{"provisioning", "", "ERROR", `{namespace="excalibase-platform",app="provisioning"} |~ "ERROR"`},
		{"watcher", testProjABC123, "", `{namespace=~".*-proj-abc123",app="excalibase-watcher"}`},
		{"deno", testProjABC123, "", `{namespace=~".*-proj-abc123",app="deno-runtime"}`},
		{"cnpg", testProjABC123, "", `{namespace=~".*-proj-abc123"} |~ "postgres"`},
		{"all", "", "", `{namespace=~"excalibase-platform|.*-proj-.*"}`},
	}
	for _, c := range cases {
		t.Run(c.svc, func(t *testing.T) {
			got, err := buildLogQL(c.svc, c.projectID, c.search)
			if err != nil {
				t.Fatalf("buildLogQL: %v", err)
			}
			if got != c.want {
				t.Errorf("svc=%s\n got: %s\nwant: %s", c.svc, got, c.want)
			}
		})
	}
}

// TestBuildLogQL_RequiresProjectIdForPerProjectServices confirms watcher,
// deno, and cnpg refuse to query without a projectId — they live in
// per-project namespaces, so a missing projectId would scan everything.
func TestBuildLogQL_RequiresProjectIdForPerProjectServices(t *testing.T) {
	for _, svc := range []string{"watcher", "deno", "cnpg"} {
		_, err := buildLogQL(svc, "", "")
		if err == nil {
			t.Errorf("%s should require projectId, got nil error", svc)
		}
	}
}

// TestBuildLogQL_RejectsUnknownService prevents typos from accidentally
// scanning every namespace via the default branch.
func TestBuildLogQL_RejectsUnknownService(t *testing.T) {
	_, err := buildLogQL("postgres", "", "")
	if err == nil {
		t.Error("unknown service should error")
	}
}

// TestBuildLogQL_RejectsBroadSearch confirms the |~ regex filter blocks
// trivially-broad patterns that would turn the proxy into a cluster-wide
// scanner, while normal substring searches still pass.
func TestBuildLogQL_RejectsBroadSearch(t *testing.T) {
	broad := []string{".*", ".+", ".", "^.*$", "(.*)", "  ", "^.+$"}
	for _, s := range broad {
		if _, err := buildLogQL("auth", "", s); err == nil {
			t.Errorf("expected broad search %q to be rejected", s)
		}
	}

	// Over-length search is rejected.
	long := strings.Repeat("a", maxLogSearchLen+1)
	if _, err := buildLogQL("auth", "", long); err == nil {
		t.Errorf("expected over-length search (%d chars) to be rejected", len(long))
	}

	// Normal substring searches still work.
	normal := []string{"ERROR", "normalsearch", "connection refused", "user=admin"}
	for _, s := range normal {
		got, err := buildLogQL("auth", "", s)
		if err != nil {
			t.Errorf("expected normal search %q to pass, got %v", s, err)
			continue
		}
		if !strings.Contains(got, "|~") {
			t.Errorf("expected search %q to be applied as |~ filter, got %s", s, got)
		}
	}
}

// TestEscapeLokiValue strips characters that could break out of a Loki
// label-matcher value. projectIds are opaque proj-* strings already, but
// this is defense in depth against future changes to the ID format.
func TestEscapeLokiValue(t *testing.T) {
	cases := map[string]string{
		testProjABC123:         testProjABC123,
		`proj"or"injection`:   "projorinjection",
		"proj-abc.123":        testProjABC123,
		"proj_with_underscore": "proj_with_underscore",
	}
	for in, want := range cases {
		got := escapeLokiValue(in)
		if got != want {
			t.Errorf("escapeLokiValue(%q): got %q, want %q", in, got, want)
		}
	}
}

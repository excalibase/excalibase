package natsauth

import (
	"slices"
	"strings"
	"testing"
)

const testStream = "CDC"

func permsOrFatal(t *testing.T, principal string) Permissions {
	t.Helper()
	perms, err := PermissionsFor(principal, testStream)
	if err != nil {
		t.Fatalf("PermissionsFor(%q): %v", principal, err)
	}
	return perms
}

func assertAllows(t *testing.T, label string, allowed []string, subject string) {
	t.Helper()
	if !slices.Contains(allowed, subject) {
		t.Errorf("%s: expected %q in %v", label, subject, allowed)
	}
}

func assertDenies(t *testing.T, label string, allowed []string, subject string) {
	t.Helper()
	if slices.Contains(allowed, subject) {
		t.Errorf("%s: did not expect %q in %v", label, subject, allowed)
	}
}

func TestPermissionsForGraphQL(t *testing.T) {
	perms := permsOrFatal(t, PrincipalGraphQL)

	assertAllows(t, "graphql sub", perms.Subscribe, "cdc.>")
	assertAllows(t, "graphql sub", perms.Subscribe, "policies.>")
	assertAllows(t, "graphql sub", perms.Subscribe, "_INBOX_svc-graphql.>")

	// The read plane never writes tenant data or reload signals.
	assertDenies(t, "graphql pub", perms.Publish, "cdc.>")
	assertDenies(t, "graphql pub", perms.Publish, "policies.>")
	assertDenies(t, "graphql pub", perms.Publish, "pgdog.>")

	// Push consumers need the JetStream consumer API and ack subjects.
	assertAllows(t, "graphql pub", perms.Publish, "$JS.API.CONSUMER.>")
	assertAllows(t, "graphql pub", perms.Publish, "$JS.ACK.>")
	// but never the stream-management API.
	assertDenies(t, "graphql pub", perms.Publish, "$JS.API.STREAM.>")
}

func TestPermissionsForProvisioning(t *testing.T) {
	perms := permsOrFatal(t, PrincipalProvisioning)

	assertAllows(t, "provisioning pub", perms.Publish, "policies.>")
	assertAllows(t, "provisioning pub", perms.Publish, "pgdog.>")
	// Also mints/verifies the shared CDC stream (nats-stream-init).
	assertAllows(t, "provisioning pub", perms.Publish, "$JS.API.STREAM.>")

	// Control plane is write-only on the bus; it must not read tenant CDC.
	assertDenies(t, "provisioning sub", perms.Subscribe, "cdc.>")
	if len(perms.Subscribe) != 1 || perms.Subscribe[0] != "_INBOX_svc-provisioning.>" {
		t.Errorf("provisioning subscribe = %v, want only its own inbox", perms.Subscribe)
	}
}

func TestPermissionsForPgDog(t *testing.T) {
	perms := permsOrFatal(t, PrincipalPgDog)

	if len(perms.Publish) != 0 {
		t.Errorf("pgdog publish = %v, want none", perms.Publish)
	}
	if len(perms.Subscribe) != 1 || perms.Subscribe[0] != SubjectPgDogReload {
		t.Errorf("pgdog subscribe = %v, want only %q", perms.Subscribe, SubjectPgDogReload)
	}
	assertDenies(t, "pgdog sub", perms.Subscribe, "pgdog.>")
	assertDenies(t, "pgdog sub", perms.Subscribe, "cdc.>")
}

func TestPermissionsForTenantWatcherIsProjectScoped(t *testing.T) {
	perms := permsOrFatal(t, TenantWatcherPrincipal("proj-a"))

	assertAllows(t, "watcher pub", perms.Publish, "cdc.proj-a.>")
	assertDenies(t, "watcher pub", perms.Publish, "cdc.proj-b.>")
	assertDenies(t, "watcher pub", perms.Publish, "cdc.>")

	// JetStream publish: verify the shared stream exists, then publish.
	assertAllows(t, "watcher pub", perms.Publish, "$JS.API.INFO")
	assertAllows(t, "watcher pub", perms.Publish, "$JS.API.STREAM.INFO."+testStream)
	assertDenies(t, "watcher pub", perms.Publish, "$JS.API.STREAM.>")

	// Reads nothing but its own publish acks.
	if len(perms.Subscribe) != 1 || perms.Subscribe[0] != "_INBOX_tw_proj-a.>" {
		t.Errorf("watcher subscribe = %v, want only its own inbox", perms.Subscribe)
	}
	assertDenies(t, "watcher sub", perms.Subscribe, "cdc.>")
	assertDenies(t, "watcher sub", perms.Subscribe, "cdc.proj-a.>")
	assertDenies(t, "watcher sub", perms.Subscribe, "policies.>")
}

func TestTenantWatchersOfDifferentProjectsDoNotOverlap(t *testing.T) {
	a := permsOrFatal(t, TenantWatcherPrincipal("alpha"))
	b := permsOrFatal(t, TenantWatcherPrincipal("beta"))

	for _, subject := range a.Publish {
		if strings.HasPrefix(subject, "cdc.") && slices.Contains(b.Publish, subject) {
			t.Errorf("tenant publish subject %q shared between projects", subject)
		}
	}
	if a.InboxPrefix == b.InboxPrefix {
		t.Errorf("inbox prefix %q shared between projects", a.InboxPrefix)
	}
}

func TestPermissionsForRejectsUnknownAndMalformedPrincipals(t *testing.T) {
	cases := []struct {
		name      string
		principal string
	}{
		{"empty", ""},
		{"unknown service", "svc-attacker"},
		{"bare prefix", tenantWatcherPrefix},
		{"wildcard project", tenantWatcherPrefix + ">"},
		{"star project", tenantWatcherPrefix + "*"},
		{"dotted project", tenantWatcherPrefix + "a.b"},
		{"spaced project", tenantWatcherPrefix + "a b"},
		{"leading dash", tenantWatcherPrefix + "-a"},
		{"too long", tenantWatcherPrefix + strings.Repeat("a", 65)},
		{"wrong separator", "tenant-watcher/proj"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PermissionsFor(tc.principal, testStream); err == nil {
				t.Errorf("PermissionsFor(%q) = nil error, want rejection", tc.principal)
			}
		})
	}
}

func TestPermissionsForRejectsUnsafeStreamName(t *testing.T) {
	if _, err := PermissionsFor(PrincipalProvisioning, "CDC.>"); err == nil {
		t.Error("stream name with wildcard accepted, want rejection")
	}
}

func TestInboxPrefixFor(t *testing.T) {
	if got := InboxPrefixFor(TenantWatcherPrincipal("proj-a")); got != "_INBOX_tw_proj-a" {
		t.Errorf("tenant inbox prefix = %q", got)
	}
	if got := InboxPrefixFor(PrincipalGraphQL); got != "_INBOX_svc-graphql" {
		t.Errorf("graphql inbox prefix = %q", got)
	}
	if got := InboxPrefixFor(PrincipalPgDog); got != "" {
		t.Errorf("pgdog inbox prefix = %q, want none", got)
	}
	if got := InboxPrefixFor("svc-attacker"); got != "" {
		t.Errorf("unknown principal inbox prefix = %q, want none", got)
	}
}

func TestProjectIDForPrincipal(t *testing.T) {
	projectID, ok := ProjectIDForPrincipal(TenantWatcherPrincipal("proj-a"))
	if !ok || projectID != "proj-a" {
		t.Errorf("ProjectIDForPrincipal = (%q, %v), want (proj-a, true)", projectID, ok)
	}
	if _, ok := ProjectIDForPrincipal(PrincipalGraphQL); ok {
		t.Error("service principal reported a project id")
	}
}

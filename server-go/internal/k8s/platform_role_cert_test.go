package k8s

import (
	"slices"
	"strings"
	"testing"
)

var platformRoles = []string{"excalibase_app", "auth_admin", "cdc_watcher"}

// firstRuleFor is the line pg_hba would pick for a network login by role to
// database kind ("all" or "replication"), skipping the in-pod loopback trust.
func firstRuleFor(lines []string, role, database string, overTLS bool) string {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 || isLoopbackLine(line) {
			continue
		}
		connType, db, user := fields[0], fields[1], fields[2]
		if connType == "hostssl" && !overTLS || connType == "hostnossl" && overTLS {
			continue
		}
		if db != "all" && db != database || db == "all" && database == "replication" {
			continue
		}
		if user != "all" && user != role {
			continue
		}
		return line
	}
	return "(operator catch-all)"
}

func methodOf(line string) string {
	fields := strings.Fields(line)
	return fields[len(fields)-1]
}

// The public port reaches Postgres SNATed, so a password is only as strong as
// its secrecy. A platform role proves itself with a certificate its cluster CA
// signed; every other network login by it is refused before CNPG's catch-all.
func TestAPlatformRoleLogsInOnlyWithACertificate(t *testing.T) {
	for _, opts := range []PostgreSQLClusterOpts{plainTLSOpts(), documentDBOpts("owner_doc")} {
		lines := hbaLines(t, opts)
		for _, role := range platformRoles {
			for _, database := range []string{"all", "replication"} {
				if got := methodOf(firstRuleFor(lines, role, database, false)); got != "reject" {
					t.Errorf("%s plaintext to %s: method %q, want reject (%q)", role, database, got, lines)
				}
				tlsRule := firstRuleFor(lines, role, database, true)
				if got := methodOf(tlsRule); got != "cert" && got != "reject" {
					t.Errorf("%s over TLS to %s is admitted by %q, want cert or reject", role, database, tlsRule)
				}
			}
			if got := methodOf(firstRuleFor(lines, role, "all", true)); got != "cert" {
				t.Errorf("%s cannot log in with its certificate: %q", role, got)
			}
		}
	}
}

func TestOnlyTheWatcherMayStreamWithItsCertificate(t *testing.T) {
	lines := hbaLines(t, plainTLSOpts())
	if got := firstRuleFor(lines, "cdc_watcher", "replication", true); got != "hostssl replication cdc_watcher all cert" {
		t.Errorf("watcher replication rule = %q", got)
	}
	for _, role := range []string{"excalibase_app", "auth_admin"} {
		if got := methodOf(firstRuleFor(lines, role, "replication", true)); got != "reject" {
			t.Errorf("%s may open a replication session: %q", role, got)
		}
	}
}

func TestTheCustomerRoleKeepsItsPassword(t *testing.T) {
	lines := hbaLines(t, plainTLSOpts())
	if got := firstRuleFor(lines, "app", "all", true); got != "hostssl all app all scram-sha-256" {
		t.Errorf("customer rule = %q", got)
	}
}

// Allowing plaintext is the customer's choice for their own role; it never
// reopens a password login for a platform role.
func TestAllowingPlaintextKeepsPlatformRolesOnCertificates(t *testing.T) {
	opts := plainTLSOpts()
	opts.AllowPlaintext = true
	lines := hbaLines(t, opts)
	for _, role := range platformRoles {
		if got := methodOf(firstRuleFor(lines, role, "all", false)); got != "reject" {
			t.Errorf("%s plaintext admitted with plaintext allowed: %q", role, got)
		}
		if got := methodOf(firstRuleFor(lines, role, "all", true)); got != "cert" {
			t.Errorf("%s over TLS: %q, want cert", role, got)
		}
	}
	if got := firstRuleFor(lines, "app", "all", false); got != "host all app all scram-sha-256" {
		t.Errorf("customer plaintext rule = %q", got)
	}
}

// "cert" is only valid on hostssl; the switch rewriting it to host would make
// the operator's pg_hba reload fail.
func TestTheTLSSwitchLeavesCertificateRulesAlone(t *testing.T) {
	cluster := BuildPostgreSQLCluster(plainTLSOpts())
	for _, requireTLS := range []bool{false, true} {
		if err := SetClusterRequireTLS(cluster, requireTLS); err != nil {
			t.Fatalf("SetClusterRequireTLS(%v): %v", requireTLS, err)
		}
		for _, line := range clusterHBA(t, cluster) {
			if methodOf(line) == "cert" && !strings.HasPrefix(line, "hostssl ") {
				t.Errorf("requireTLS=%v rewrote %q", requireTLS, line)
			}
		}
		if !slices.Contains(clusterHBA(t, cluster), "host all excalibase_app all reject") {
			t.Errorf("requireTLS=%v dropped a platform reject: %q", requireTLS, clusterHBA(t, cluster))
		}
	}
}

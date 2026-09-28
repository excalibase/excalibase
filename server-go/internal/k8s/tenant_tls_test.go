package k8s

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func plainTLSOpts() PostgreSQLClusterOpts {
	opts := documentDBOpts("owner_tls")
	opts.DocumentDB = false
	return opts
}

func isLoopbackLine(line string) bool {
	return strings.Contains(line, " 127.0.0.1/32 ") || strings.Contains(line, " ::1/128 ")
}

func TestEveryNetworkLoginRequiresTLSByDefault(t *testing.T) {
	want := []string{
		"hostssl replication cdc_watcher all scram-sha-256",
		"hostssl all app all scram-sha-256",
		"hostssl all excalibase_app all scram-sha-256",
		"hostssl all auth_admin all scram-sha-256",
		"hostnossl all all all reject",
		"hostnossl replication all all reject",
	}
	if got := hbaLines(t, plainTLSOpts()); !slices.Equal(got, want) {
		t.Errorf("pg_hba:\n got %q\nwant %q", got, want)
	}
}

func TestAllowingPlaintextAcceptsUnencryptedLogins(t *testing.T) {
	opts := plainTLSOpts()
	opts.AllowPlaintext = true
	for _, line := range hbaLines(t, opts) {
		if !strings.HasPrefix(line, "host ") {
			t.Errorf("with plaintext allowed, %q should be a host line", line)
		}
	}
}

// CNPG appends "host all all all scram-sha-256" after our rules, so a
// plaintext login that skips every hostssl line would still be let in there.
func TestPlaintextIsRejectedBeforeTheOperatorsCatchAll(t *testing.T) {
	lines := hbaLines(t, plainTLSOpts())
	for _, want := range []string{"hostnossl all all all reject", "hostnossl replication all all reject"} {
		if !slices.Contains(lines, want) {
			t.Errorf("missing %q in %q", want, lines)
		}
	}
	if last := lines[len(lines)-1]; !strings.HasSuffix(last, " reject") {
		t.Errorf("the reject must come after every login rule, last line is %q", last)
	}
}

// The DocumentDB gateway reaches Postgres over loopback inside the pod; that
// hop never leaves the pod and stays a host line either way.
func TestLoopbackTrustIsNotTurnedIntoATLSRule(t *testing.T) {
	for _, line := range hbaLines(t, documentDBOpts("owner_doc")) {
		if isLoopbackLine(line) && !strings.HasPrefix(line, "host ") {
			t.Errorf("loopback line changed: %q", line)
		}
		if strings.HasSuffix(line, " reject") {
			continue
		}
		if !isLoopbackLine(line) && !strings.HasPrefix(line, "hostssl ") {
			t.Errorf("network line does not require TLS: %q", line)
		}
	}
}

func TestARestoredClusterRequiresTLS(t *testing.T) {
	cluster, err := BuildRestoreCluster(RestoreClusterOpts{
		Cluster:         plainTLSOpts(),
		SourceProjectID: "proj-source01",
		Store:           ObjectStoreOpts{Bucket: "b", SecretName: "s"},
	})
	if err != nil {
		t.Fatalf("BuildRestoreCluster: %v", err)
	}
	if !ClusterRequiresTLS(cluster) {
		t.Errorf("restored cluster accepts plaintext: %q", clusterHBA(t, cluster))
	}
}

func clusterHBA(t *testing.T, cluster *unstructured.Unstructured) []string {
	t.Helper()
	lines, found, err := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	if err != nil || !found {
		t.Fatalf("cluster pg_hba: found=%v err=%v", found, err)
	}
	return lines
}

func TestSetClusterRequireTLSRewritesOnlyTheNetworkLines(t *testing.T) {
	cluster := BuildPostgreSQLCluster(documentDBOpts("owner_doc"))
	before := clusterHBA(t, cluster)

	if err := SetClusterRequireTLS(cluster, false); err != nil {
		t.Fatalf("SetClusterRequireTLS(false): %v", err)
	}
	off := clusterHBA(t, cluster)
	var want []string
	for _, line := range before {
		if !slices.Contains(plaintextRejects, line) {
			want = append(want, strings.Replace(line, "hostssl ", "host ", 1))
		}
	}
	if !slices.Equal(off, want) {
		t.Errorf("plaintext allowed:\n got %q\nwant %q", off, want)
	}
	if ClusterRequiresTLS(cluster) {
		t.Error("cluster still reported as requiring TLS")
	}

	if err := SetClusterRequireTLS(cluster, true); err != nil {
		t.Fatalf("SetClusterRequireTLS(true): %v", err)
	}
	if got := clusterHBA(t, cluster); !slices.Equal(got, before) {
		t.Errorf("round trip:\n got %q\nwant %q", got, before)
	}
	if !ClusterRequiresTLS(cluster) {
		t.Error("cluster not reported as requiring TLS")
	}
}

func TestAClusterWithHostSSLButNoRejectDoesNotCountAsRequiringTLS(t *testing.T) {
	cluster := BuildPostgreSQLCluster(plainTLSOpts())
	lines := clusterHBA(t, cluster)
	kept := make([]interface{}, 0, len(lines))
	for _, line := range lines {
		if !strings.HasSuffix(line, " reject") {
			kept = append(kept, line)
		}
	}
	if err := unstructured.SetNestedSlice(cluster.Object, kept, "spec", "postgresql", "pg_hba"); err != nil {
		t.Fatal(err)
	}
	if ClusterRequiresTLS(cluster) {
		t.Fatal("hostssl without the reject lets plaintext reach the operator's catch-all")
	}
}

func TestSetClusterRequireTLSRefusesAClusterWithNoRules(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	if err := SetClusterRequireTLS(cluster, true); err == nil {
		t.Fatal("a cluster with no pg_hba must be refused, not left to the operator's defaults")
	}
}

// The Mongo user reject is not a login: the TLS switch neither narrows it to
// hostssl (which would let a plaintext attempt fall through) nor drops it.
func TestTheTLSSwitchLeavesTheMongoUserRejectAlone(t *testing.T) {
	const reject = "host all +excalibase_mongo_users all reject"
	cluster := BuildPostgreSQLCluster(documentDBOpts("owner_doc"))
	for _, requireTLS := range []bool{false, true} {
		if err := SetClusterRequireTLS(cluster, requireTLS); err != nil {
			t.Fatalf("SetClusterRequireTLS(%v): %v", requireTLS, err)
		}
		if !slices.Contains(clusterHBA(t, cluster), reject) {
			t.Errorf("requireTLS=%v lost or rewrote the Mongo user reject: %q", requireTLS, clusterHBA(t, cluster))
		}
	}
	if !ClusterRequiresTLS(cluster) {
		t.Error("the reject line stopped the cluster counting as requiring TLS")
	}
}

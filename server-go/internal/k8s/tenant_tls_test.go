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

// The DocumentDB gateway reaches Postgres over loopback inside the pod; that
// hop never leaves the pod and stays a host line either way.
func TestLoopbackTrustIsNotTurnedIntoATLSRule(t *testing.T) {
	for _, line := range hbaLines(t, documentDBOpts("owner_doc")) {
		if isLoopbackLine(line) && !strings.HasPrefix(line, "host ") {
			t.Errorf("loopback line changed: %q", line)
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
	for _, line := range clusterHBA(t, cluster) {
		if !strings.HasPrefix(line, "hostssl ") {
			t.Errorf("restored cluster accepts plaintext: %q", line)
		}
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
	if len(off) != len(before) {
		t.Fatalf("line count changed: %q -> %q", before, off)
	}
	for i, line := range off {
		want := strings.Replace(before[i], "hostssl ", "host ", 1)
		if line != want {
			t.Errorf("line %d: got %q want %q", i, line, want)
		}
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

func TestSetClusterRequireTLSRefusesAClusterWithNoRules(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	if err := SetClusterRequireTLS(cluster, true); err == nil {
		t.Fatal("a cluster with no pg_hba must be refused, not left to the operator's defaults")
	}
}

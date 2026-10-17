package k8s

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func clusterWithHBA(lines ...string) *unstructured.Unstructured {
	values := make([]interface{}, 0, len(lines))
	for _, line := range lines {
		values = append(values, line)
	}
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{}}
	_ = unstructured.SetNestedSlice(cluster.Object, values, "spec", "postgresql", "pg_hba")
	return cluster
}

func hbaOf(t *testing.T, cluster *unstructured.Unstructured) []string {
	t.Helper()
	lines, _, err := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// A cluster rendered before excalibase_scheduler existed gets its certificate
// login and rejects where the other platform roles' are, ahead of every
// password login (EXC-623).
func TestEnsurePlatformCertLogins_AddsAMissingRoleBeforeThePasswordLogins(t *testing.T) {
	loopback := "host all documentdb 127.0.0.1/32 trust"
	cluster := clusterWithHBA(loopback,
		"hostssl all excalibase_app all cert",
		"hostssl all auth_admin all cert",
		"hostssl all cdc_watcher all cert",
		"hostssl replication cdc_watcher all cert",
		"host all excalibase_app all reject",
		"host replication excalibase_app all reject",
		"host all auth_admin all reject",
		"host replication auth_admin all reject",
		"host all cdc_watcher all reject",
		"host replication cdc_watcher all reject",
		"hostssl all app all scram-sha-256",
		"hostnossl all all all reject",
	)
	changed, err := EnsurePlatformCertLogins(cluster)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := hbaOf(t, cluster)
	want := append(append([]string{loopback}, toStrings(platformCertLogins())...),
		"hostssl all app all scram-sha-256", "hostnossl all all all reject")
	if !slices.Equal(got, want) {
		t.Fatalf("pg_hba:\n got %q\nwant %q", got, want)
	}
	if !slices.Contains(got, "hostssl all excalibase_scheduler all cert") || !slices.Contains(got, "host all excalibase_scheduler all reject") {
		t.Fatal("excalibase_scheduler has no certificate login")
	}

	again, err := EnsurePlatformCertLogins(cluster)
	if err != nil || again {
		t.Fatalf("a second pass changed the rules: changed=%v err=%v", again, err)
	}
}

// A cluster from before certificate logins keeps its rules: rejecting the
// platform roles' passwords there would lock out records with no certificate.
func TestEnsurePlatformCertLogins_LeavesAClusterWithoutCertLoginsAlone(t *testing.T) {
	cluster := clusterWithHBA("hostssl all app all scram-sha-256")
	if changed, err := EnsurePlatformCertLogins(cluster); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := hbaOf(t, cluster); !slices.Equal(got, []string{"hostssl all app all scram-sha-256"}) {
		t.Fatalf("pg_hba = %q", got)
	}
}

func TestEnsurePlatformCertLogins_RefusesAClusterWithoutRules(t *testing.T) {
	if _, err := EnsurePlatformCertLogins(&unstructured.Unstructured{Object: map[string]interface{}{}}); err != ErrClusterHasNoHBA {
		t.Fatalf("err = %v", err)
	}
}

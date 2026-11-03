package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-654: with the feature off a cluster renders exactly as before, so no
// running tenant is restarted by a release that only carries the code.
func TestQueryStatsOff_TheClusterNamesNoStatementStatistics(t *testing.T) {
	postgresql := postgresqlSection(t, clusterOpts("p1", "org-p1"))
	params := postgresql["parameters"].(map[string]interface{})
	for name := range params {
		if strings.HasPrefix(name, "pg_stat_statements") {
			t.Errorf("feature off renders %s", name)
		}
	}
	if _, set := postgresql["shared_preload_libraries"]; set {
		t.Errorf("feature off sets shared_preload_libraries: %v", postgresql["shared_preload_libraries"])
	}
}

// CloudNativePG preloads the library and creates the extension itself once a
// pg_stat_statements.* parameter is set; the platform names no library.
func TestQueryStatsOn_SetsTheParametersCNPGManagesTheLibraryBy(t *testing.T) {
	opts := clusterOpts("p1", "org-p1")
	opts.QueryStats = true
	postgresql := postgresqlSection(t, opts)
	params := postgresql["parameters"].(map[string]interface{})
	want := map[string]string{
		"pg_stat_statements.max":           "5000",
		"pg_stat_statements.track":         "top",
		"pg_stat_statements.track_utility": "off",
	}
	for name, value := range want {
		if params[name] != value {
			t.Errorf("%s = %v, want %s", name, params[name], value)
		}
	}
	if _, set := postgresql["shared_preload_libraries"]; set {
		t.Errorf("the library is CNPG's to add: %v", postgresql["shared_preload_libraries"])
	}
}

// A DocumentDB cluster keeps its own preload list; CNPG merges the managed one.
func TestQueryStatsOn_KeepsDocumentDBsLibraries(t *testing.T) {
	opts := PostgreSQLClusterOpts{ProjectID: "p2", Namespace: "org-p2", Tier: documentDBTier(), DatabaseName: "appdb", DocumentDB: true, QueryStats: true}
	libraries := preloadedLibraries(t, postgresqlSection(t, opts))
	if !listHas(libraries, "pg_documentdb") || listHas(libraries, "pg_stat_statements") {
		t.Errorf("libraries = %v", libraries)
	}
}

// A tenant cannot turn the statistics off, or size them, through its own settings.
func TestQueryStats_ATenantParameterCannotOverrideThem(t *testing.T) {
	opts := clusterOpts("p1", "org-p1")
	opts.QueryStats = true
	opts.Parameters = map[string]string{"pg_stat_statements.track": "none", "pg_stat_statements.max": "100"}
	params := postgresqlSection(t, opts)["parameters"].(map[string]interface{})
	if params["pg_stat_statements.track"] != "top" || params["pg_stat_statements.max"] != "5000" {
		t.Errorf("tenant overrode the statistics: %v", params)
	}
	off := clusterOpts("p1", "org-p1")
	off.Parameters = map[string]string{"pg_stat_statements.track": "all"}
	if _, set := postgresqlSection(t, off)["parameters"].(map[string]interface{})["pg_stat_statements.track"]; set {
		t.Error("a tenant parameter turned the statistics on")
	}
}

func TestWithQueryStats_AddsTheParametersAndKeepsEverythingElse(t *testing.T) {
	opts := clusterOpts("p1", "org-p1")
	opts.Parameters = map[string]string{"work_mem": "8MB"}
	cluster := BuildPostgreSQLCluster(opts)
	if ClusterHasQueryStats(cluster) {
		t.Fatal("a cluster built with the feature off reports statistics")
	}
	changed := WithQueryStats(cluster)
	if !ClusterHasQueryStats(changed) {
		t.Fatal("WithQueryStats did not turn them on")
	}
	if ClusterHasQueryStats(cluster) {
		t.Error("WithQueryStats changed the cluster it was given")
	}
	params, _, _ := unstructured.NestedMap(changed.Object, "spec", "postgresql", "parameters")
	if params["work_mem"] != "8MB" || params["max_connections"] != "100" {
		t.Errorf("other settings were lost: %v", params)
	}
}

// Tuning, a tier change and a resize keep what the platform set.
func TestQueryStats_SurviveLaterClusterChanges(t *testing.T) {
	opts := clusterOpts("p1", "org-p1")
	opts.QueryStats = true
	cluster := BuildPostgreSQLCluster(opts)
	for name, changed := range map[string]*unstructured.Unstructured{
		"tuning": WithTenantParameters(cluster, map[string]string{"work_mem": "16MB"}),
		"tier":   WithTier(cluster, standardTier(), "50Gi"),
		"resize": WithStorageSize(cluster, "60Gi"),
	} {
		if !ClusterHasQueryStats(changed) {
			t.Errorf("%s dropped the statistics", name)
		}
	}
}

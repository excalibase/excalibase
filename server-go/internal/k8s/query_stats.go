package k8s

import (
	"maps"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// queryStatsParameters turn on pg_stat_statements (EXC-654). CloudNativePG
// adds the library to shared_preload_libraries and creates the extension in
// every database once a pg_stat_statements.* parameter is set, and drops it
// again when none is. Utility statements are not tracked: their text is not
// normalized, so an ALTER ROLE ... PASSWORD would be kept verbatim.
func queryStatsParameters() map[string]interface{} {
	return map[string]interface{}{
		"pg_stat_statements.max":           "5000",
		"pg_stat_statements.track":         "top",
		"pg_stat_statements.track_utility": "off",
	}
}

// ClusterHasQueryStats reports whether the cluster carries the statistics settings.
func ClusterHasQueryStats(cluster *unstructured.Unstructured) bool {
	params, _, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "parameters")
	for name, value := range queryStatsParameters() {
		if params[name] != value {
			return false
		}
	}
	return true
}

// WithQueryStats is a copy of the cluster with the statistics settings added.
// Changing them restarts the database once.
func WithQueryStats(cluster *unstructured.Unstructured) *unstructured.Unstructured {
	changed := cluster.DeepCopy()
	spec, _, _ := unstructured.NestedMap(changed.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}
	params := postgresParameters(spec)
	maps.Copy(params, queryStatsParameters())
	setPostgresParameters(spec, params)
	_ = unstructured.SetNestedMap(changed.Object, spec, "spec")
	return changed
}

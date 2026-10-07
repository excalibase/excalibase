package k8s

import (
	"errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrClusterHasNoArchive refuses an in-place recovery of a cluster that
// archives its WAL nowhere: there is nothing to recover it from.
var ErrClusterHasNoArchive = errors.New("the project's database archives no backups to recover from")

// BuildInPlaceRecovery is live recreated under its own name, bootstrapped by
// recovering its own archive to target (nil: the end of the archive). Every
// other setting is kept as it runs now — resources, parameters, storage,
// certificates, plugins — and the recovered cluster archives on into the same
// store on a new timeline. live is not modified.
func BuildInPlaceRecovery(live *unstructured.Unstructured, target map[string]interface{}) (*unstructured.Unstructured, error) {
	spec, _, _ := unstructured.NestedMap(live.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}
	archive, ok := walArchiveParameters(spec)
	if !ok {
		return nil, ErrClusterHasNoArchive
	}
	recovery := map[string]interface{}{"source": recoverySourceName}
	for _, key := range []string{"database", "owner"} {
		if value := bootstrapSetting(spec, key); value != "" {
			recovery[key] = value
		}
	}
	if target != nil {
		recovery["recoveryTarget"] = target
	}
	spec["bootstrap"] = map[string]interface{}{"recovery": recovery}
	spec["externalClusters"] = []interface{}{map[string]interface{}{
		"name":   recoverySourceName,
		"plugin": map[string]interface{}{"name": BarmanCloudPluginName, "parameters": archive},
	}}

	metadata := map[string]interface{}{"name": live.GetName(), "namespace": live.GetNamespace()}
	if labels := live.GetLabels(); len(labels) > 0 {
		metadata["labels"] = stringMap(labels)
	}
	annotations := live.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[SkipEmptyWalArchiveCheckAnnotation] = "enabled"
	metadata["annotations"] = stringMap(annotations)

	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": live.GetAPIVersion(),
		"kind":       live.GetKind(),
		"metadata":   metadata,
		"spec":       spec,
	}}, nil
}

// walArchiveParameters are the archiving plugin's store and server name.
func walArchiveParameters(spec map[string]interface{}) (map[string]interface{}, bool) {
	plugins, _ := spec["plugins"].([]interface{})
	for _, raw := range plugins {
		plugin, _ := raw.(map[string]interface{})
		if archiver, _ := plugin["isWALArchiver"].(bool); !archiver || plugin["name"] != BarmanCloudPluginName {
			continue
		}
		params, _ := plugin["parameters"].(map[string]interface{})
		if params["barmanObjectName"] == nil {
			return nil, false
		}
		return map[string]interface{}{"barmanObjectName": params["barmanObjectName"], "serverName": params["serverName"]}, true
	}
	return nil, false
}

// bootstrapSetting is the database or owner the cluster was bootstrapped with,
// by initdb or by an earlier recovery.
func bootstrapSetting(spec map[string]interface{}, key string) string {
	for _, method := range []string{"initdb", "recovery"} {
		if value, _, _ := unstructured.NestedString(spec, "bootstrap", method, key); value != "" {
			return value
		}
	}
	return ""
}

func stringMap(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

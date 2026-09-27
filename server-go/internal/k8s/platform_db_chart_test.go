package k8s

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	platformBaseChartDir    = "../../../charts/platform-base"
	barmanCloudPlugin       = "barman-cloud.cloudnative-pg.io"
	barmanCloudStoreVersion = "barmancloud.cnpg.io/v1"
)

func renderPlatformBase(t *testing.T, overrides map[string]interface{}) ([]*unstructured.Unstructured, error) {
	t.Helper()
	chart, err := loader.Load(platformBaseChartDir)
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	options := chartutil.ReleaseOptions{Name: "platform-base", Namespace: "excalibase-platform", IsInstall: true}
	values, err := chartutil.ToRenderValues(chart, map[string]interface{}{"platformDB": overrides}, options, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("render values: %v", err)
	}
	manifests, err := engine.Render(chart, values)
	if err != nil {
		return nil, err
	}
	var objects []*unstructured.Unstructured
	for name, manifest := range manifests {
		for _, document := range strings.Split(manifest, "\n---") {
			var object unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(document), &object.Object); err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if object.GetKind() != "" {
				objects = append(objects, &object)
			}
		}
	}
	return objects, nil
}

func renderedOfKind(objects []*unstructured.Unstructured, kind string) []*unstructured.Unstructured {
	var matching []*unstructured.Unstructured
	for _, object := range objects {
		if object.GetKind() == kind {
			matching = append(matching, object)
		}
	}
	return matching
}

func renderPlatformDB(t *testing.T, overrides map[string]interface{}) (*unstructured.Unstructured, error) {
	t.Helper()
	objects, err := renderPlatformBase(t, overrides)
	if err != nil {
		return nil, err
	}
	clusters := renderedOfKind(objects, "Cluster")
	if len(clusters) != 1 {
		t.Fatalf("the chart rendered %d platform-db Clusters, want 1", len(clusters))
	}
	return clusters[0], nil
}

func renderPlatformDBObjectStore(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	objects, err := renderPlatformBase(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	stores := renderedOfKind(objects, "ObjectStore")
	if len(stores) != 1 {
		t.Fatalf("the chart rendered %d ObjectStores, want 1", len(stores))
	}
	return stores[0]
}

func withEndpoint() map[string]interface{} {
	return map[string]interface{}{"backup": map[string]interface{}{"endpointURL": "https://account.r2.cloudflarestorage.com"}}
}

func TestThePlatformDatabaseBacksUpWithAKeyOfItsOwn(t *testing.T) {
	store, _, _ := unstructured.NestedMap(renderPlatformDBObjectStore(t).Object, "spec", "configuration")
	for _, key := range []string{"accessKeyId", "secretAccessKey"} {
		secret, _, _ := unstructured.NestedString(store, "s3Credentials", key, "name")
		if secret == "" || secret == "r2-creds" || secret == "backup-s3-creds" {
			t.Errorf("%s comes from %q, a key tenant namespaces hold", key, secret)
		}
	}
	destination, _, _ := unstructured.NestedString(store, "destinationPath")
	if strings.HasPrefix(destination, "s3://excalibase-backups/") {
		t.Errorf("platform-db backups share the tenant backup bucket: %s", destination)
	}
}

func TestThePlatformDatabaseImageIsPinnedByDigest(t *testing.T) {
	cluster, err := renderPlatformDB(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	if !strings.Contains(image, "@sha256:") {
		t.Errorf("imageName %q is not pinned by digest", image)
	}
}

func TestThePlatformDatabaseRefusesATenantKey(t *testing.T) {
	for _, secret := range []string{"r2-creds", "backup-s3-creds"} {
		overrides := withEndpoint()
		overrides["backup"].(map[string]interface{})["s3Secret"] = secret
		if _, err := renderPlatformDB(t, overrides); err == nil {
			t.Errorf("rendered platform-db backups on the tenant key %q", secret)
		}
	}
}

func TestThePlatformDatabaseRefusesBackupsWithoutAnEndpoint(t *testing.T) {
	if _, err := renderPlatformDB(t, map[string]interface{}{}); err == nil {
		t.Error("rendered platform-db backups with no object store endpoint")
	}
}

func TestThePlatformDatabaseHasNoInTreeBarmanBackup(t *testing.T) {
	objects, err := renderPlatformBase(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, object := range objects {
		manifest, _ := yaml.Marshal(object.Object)
		if strings.Contains(string(manifest), "barmanObjectStore") {
			t.Errorf("%s/%s still uses the in-tree barmanObjectStore", object.GetKind(), object.GetName())
		}
	}
}

func TestThePlatformDatabaseObjectStoreUsesThePlatformKey(t *testing.T) {
	store := renderPlatformDBObjectStore(t)
	if store.GetAPIVersion() != barmanCloudStoreVersion || store.GetNamespace() != "excalibase-platform" {
		t.Errorf("ObjectStore is %s in %q", store.GetAPIVersion(), store.GetNamespace())
	}
	for _, key := range []string{"accessKeyId", "secretAccessKey"} {
		secret, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "s3Credentials", key, "name")
		if secret != "platform-db-backup-creds" {
			t.Errorf("%s comes from %q, want platform-db-backup-creds", key, secret)
		}
	}
	endpoint, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "endpointURL")
	if endpoint != "https://account.r2.cloudflarestorage.com" {
		t.Errorf("endpointURL: got %q", endpoint)
	}
}

func TestThePlatformDatabaseArchivesWALThroughThePlugin(t *testing.T) {
	cluster, err := renderPlatformDB(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	store := renderPlatformDBObjectStore(t)
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	if len(plugins) != 1 {
		t.Fatalf("spec.plugins: got %v", plugins)
	}
	plugin := plugins[0].(map[string]interface{})
	objectName, _, _ := unstructured.NestedString(plugin, "parameters", "barmanObjectName")
	if plugin["name"] != barmanCloudPlugin || plugin["isWALArchiver"] != true || objectName != store.GetName() {
		t.Errorf("the cluster does not archive to %s through the plugin: %v", store.GetName(), plugin)
	}
}

func TestThePlatformDatabaseIsBackedUpOnScheduleByThePlugin(t *testing.T) {
	objects, err := renderPlatformBase(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	scheduled := renderedOfKind(objects, "ScheduledBackup")
	if len(scheduled) != 1 {
		t.Fatalf("the chart rendered %d ScheduledBackups, want 1", len(scheduled))
	}
	spec, _, _ := unstructured.NestedMap(scheduled[0].Object, "spec")
	plugin, _, _ := unstructured.NestedString(spec, "pluginConfiguration", "name")
	cluster, _, _ := unstructured.NestedString(spec, "cluster", "name")
	if spec["method"] != "plugin" || plugin != barmanCloudPlugin || cluster != "platform-db" {
		t.Errorf("scheduled backup is not taken by the plugin: %v", spec)
	}
	if err := ValidateBackupSchedule(spec["schedule"].(string)); err != nil {
		t.Errorf("schedule: %v", err)
	}
}

func TestThePlatformDatabaseWithoutBackupsRendersNoPluginObjects(t *testing.T) {
	objects, err := renderPlatformBase(t, map[string]interface{}{"backup": map[string]interface{}{"enabled": false}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(renderedOfKind(objects, "ObjectStore"))+len(renderedOfKind(objects, "ScheduledBackup")) != 0 {
		t.Error("backups disabled still renders an ObjectStore or ScheduledBackup")
	}
	cluster := renderedOfKind(objects, "Cluster")[0]
	if _, present, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins"); present {
		t.Error("backups disabled still registers the plugin on the cluster")
	}
}

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

const platformBaseChartDir = "../../../charts/platform-base"

func renderPlatformDB(t *testing.T, overrides map[string]interface{}) (*unstructured.Unstructured, error) {
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
	for name, manifest := range manifests {
		if !strings.HasSuffix(name, "platform-db.yaml") {
			continue
		}
		var cluster unstructured.Unstructured
		if err := yaml.Unmarshal([]byte(manifest), &cluster.Object); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return &cluster, nil
	}
	t.Fatal("the chart rendered no platform-db Cluster")
	return nil, nil
}

func withEndpoint() map[string]interface{} {
	return map[string]interface{}{"backup": map[string]interface{}{"endpointURL": "https://account.r2.cloudflarestorage.com"}}
}

func TestThePlatformDatabaseBacksUpWithAKeyOfItsOwn(t *testing.T) {
	cluster, err := renderPlatformDB(t, withEndpoint())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	store, _, _ := unstructured.NestedMap(cluster.Object, "spec", "backup", "barmanObjectStore")
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

package k8s

import (
	"errors"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func standardTier() config.TierConfig {
	return config.TierConfig{Instances: 1, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "30s"}
}

func clusterOpts(projectID, namespace string) PostgreSQLClusterOpts {
	return PostgreSQLClusterOpts{ProjectID: projectID, Namespace: namespace, Tier: standardTier()}
}

// fullProjectCluster is a project with everything a new cluster can carry
// apart from its bootstrap: tenant parameters, a storage class, backups.
func fullProjectCluster() PostgreSQLClusterOpts {
	opts := clusterOpts("dst", "org-dst")
	opts.Tier = config.TierConfig{Instances: 3, StorageSize: "500Gi", Memory: "16Gi", CPU: "4", StatementTimeout: "60s"}
	opts.StorageClass = "fast-ssd"
	opts.Parameters = map[string]string{"work_mem": "64MB", "shared_preload_libraries": "pg_stat_statements"}
	opts.Backup = &BackupOpts{Schedule: "0 0 2 * * *", RetentionDays: 14, EndpointURL: testR2Endpoint, Bucket: "excalibase-backups"}
	opts.ImageName = "excalibase/postgresql@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	opts.ServerAltDNSNames = []string{"dst.db.example.com"}
	opts.DatabaseName = "shop"
	opts.MasterUsername = "owner"
	return opts
}

func restoreOf(cluster PostgreSQLClusterOpts) RestoreClusterOpts {
	return RestoreClusterOpts{
		Cluster:         cluster,
		SourceProjectID: "src",
		Store:           ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "excalibase-backups", SecretName: "backup-s3-creds"},
		RecoveryTarget:  map[string]interface{}{"targetTime": "2026-01-01T00:00:00Z"},
	}
}

func mustBuildRestore(t *testing.T, opts RestoreClusterOpts) *unstructured.Unstructured {
	t.Helper()
	obj, err := BuildRestoreCluster(opts)
	if err != nil {
		t.Fatalf("BuildRestoreCluster: %v", err)
	}
	return obj
}

func withoutBootstrap(obj *unstructured.Unstructured) map[string]interface{} {
	copied := obj.DeepCopy().Object
	spec := copied["spec"].(map[string]interface{})
	delete(spec, "bootstrap")
	delete(spec, "externalClusters")
	return copied
}

func TestRestoreClusterIsANewClusterSaveForItsBootstrap(t *testing.T) {
	cluster := fullProjectCluster()
	restored := mustBuildRestore(t, restoreOf(cluster))
	fresh := BuildPostgreSQLCluster(cluster)

	if got, want := withoutBootstrap(restored), withoutBootstrap(fresh); !reflect.DeepEqual(got, want) {
		gotYAML, _ := yaml.Marshal(got)
		wantYAML, _ := yaml.Marshal(want)
		t.Errorf("a restored cluster must render as a new one of the same project\n--- restored ---\n%s\n--- new ---\n%s", gotYAML, wantYAML)
	}
	bootstrap, _, _ := unstructured.NestedMap(restored.Object, "spec", "bootstrap")
	if len(bootstrap) != 1 || bootstrap["recovery"] == nil {
		t.Errorf("a restored cluster bootstraps by recovery alone, got %v", bootstrap)
	}
	database, _, _ := unstructured.NestedString(restored.Object, "spec", "bootstrap", "recovery", "database")
	owner, _, _ := unstructured.NestedString(restored.Object, "spec", "bootstrap", "recovery", "owner")
	if database != "shop" || owner != "owner" {
		t.Errorf("recovery must name the project's own database and owner, got %q / %q", database, owner)
	}
}

func TestRestoreClusterLeavesTheDefaultDatabaseUnnamed(t *testing.T) {
	restored := mustBuildRestore(t, restoreOf(clusterOpts("dst", "org-dst")))
	recovery, _, _ := unstructured.NestedMap(restored.Object, "spec", "bootstrap", "recovery")
	if _, named := recovery["database"]; named {
		t.Errorf("a project on the default database names none, as a new one does: %v", recovery)
	}
}

func TestRestoreClusterRecoversFromTheSourceAndBacksUpToItself(t *testing.T) {
	spec := mustBuildRestore(t, restoreOf(fullProjectCluster())).Object["spec"].(map[string]interface{})

	if source := barmanStoreOf(t, spec)["destinationPath"]; source != "s3://excalibase-backups/src" {
		t.Errorf("recovery must read the source's backups, got %v", source)
	}
	target, _, _ := unstructured.NestedString(spec, "backup", "barmanObjectStore", "destinationPath")
	if target != "s3://excalibase-backups/dst" {
		t.Errorf("the restored project must back up under its own prefix, got %q", target)
	}
}

func TestBuildRestoreClusterRefusesAnIncompleteTier(t *testing.T) {
	cases := map[string]func(*config.TierConfig){
		"no tier at all":   func(tier *config.TierConfig) { *tier = config.TierConfig{} },
		"no instances":     func(tier *config.TierConfig) { tier.Instances = 0 },
		"no storage size":  func(tier *config.TierConfig) { tier.StorageSize = "" },
		"no memory":        func(tier *config.TierConfig) { tier.Memory = "" },
		"no cpu":           func(tier *config.TierConfig) { tier.CPU = "" },
		"negative replica": func(tier *config.TierConfig) { tier.Instances = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cluster := clusterOpts("dst", "org-dst")
			mutate(&cluster.Tier)
			obj, err := BuildRestoreCluster(restoreOf(cluster))
			if !errors.Is(err, ErrTierSizingIncomplete) {
				t.Fatalf("err: got %v, want ErrTierSizingIncomplete", err)
			}
			if obj != nil {
				t.Error("no cluster may be rendered for an incomplete tier")
			}
		})
	}
}

func TestRestoreClusterGolden(t *testing.T) {
	encoded, err := yaml.Marshal(mustBuildRestore(t, restoreOf(fullProjectCluster())).Object)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	assertGoldenAt(t, "testdata/restore_cluster/standard-pitr.yaml", string(encoded))
}

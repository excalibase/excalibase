package k8s

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// liveCluster is a project's cluster as the API server returns it: the
// platform's spec plus everything the server and operator added.
func liveCluster(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	opts := fullProjectCluster()
	obj := BuildPostgreSQLCluster(opts)
	spec := obj.Object["spec"].(map[string]interface{})
	spec["bootstrap"] = map[string]interface{}{"initdb": map[string]interface{}{"database": "shop", "owner": "owner"}}
	spec["maintenanceWindow"] = map[string]interface{}{"inProgress": false}
	meta := obj.Object["metadata"].(map[string]interface{})
	meta["uid"] = "c0ffee"
	meta["resourceVersion"] = "42"
	meta["generation"] = int64(7)
	meta["creationTimestamp"] = "2026-10-01T00:00:00Z"
	meta["finalizers"] = []interface{}{"cnpg.io/cleanup"}
	meta["managedFields"] = []interface{}{map[string]interface{}{"manager": "kubectl"}}
	meta["labels"] = map[string]interface{}{"excalibase.io/project": "dst"}
	obj.Object["status"] = map[string]interface{}{"phase": "Cluster in healthy state"}
	return obj
}

func TestBuildInPlaceRecoveryRecoversTheClusterFromItsOwnArchive(t *testing.T) {
	live := liveCluster(t)
	target := map[string]interface{}{"targetTime": "2026-10-07T12:00:00Z"}

	got, err := BuildInPlaceRecovery(live, target)
	if err != nil {
		t.Fatalf("BuildInPlaceRecovery: %v", err)
	}
	spec := got.Object["spec"].(map[string]interface{})
	recovery := spec["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	want := map[string]interface{}{"source": recoverySourceName, "database": "shop", "owner": "owner", "recoveryTarget": target}
	if !reflect.DeepEqual(recovery, want) {
		t.Errorf("recovery:\n got %v\nwant %v", recovery, want)
	}
	external := spec["externalClusters"].([]interface{})
	wantSource := map[string]interface{}{
		"name": recoverySourceName,
		"plugin": map[string]interface{}{
			"name":       BarmanCloudPluginName,
			"parameters": map[string]interface{}{"barmanObjectName": "dst-backups", "serverName": "cloud"},
		},
	}
	if len(external) != 1 || !reflect.DeepEqual(external[0], wantSource) {
		t.Errorf("externalClusters: %v", external)
	}
}

func TestBuildInPlaceRecoveryKeepsTheClusterAsItWas(t *testing.T) {
	live := liveCluster(t)
	got, err := BuildInPlaceRecovery(live, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveSpec := live.Object["spec"].(map[string]interface{})
	spec := got.Object["spec"].(map[string]interface{})
	for _, field := range []string{"resources", "postgresql", "storage", "plugins", "imageName", "instances", "certificates", "maintenanceWindow"} {
		if !reflect.DeepEqual(spec[field], liveSpec[field]) {
			t.Errorf("%s changed:\n got %v\nwant %v", field, spec[field], liveSpec[field])
		}
	}
	if _, has := spec["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})["recoveryTarget"]; has {
		t.Error("no target means recover to the end of the archive")
	}
	if got.GetName() != "dst-postgres" || got.GetNamespace() != "org-dst" {
		t.Errorf("identity: %s/%s", got.GetNamespace(), got.GetName())
	}
	if got.GetLabels()["excalibase.io/project"] != "dst" {
		t.Errorf("labels: %v", got.GetLabels())
	}
	if got.GetAnnotations()[SkipEmptyWalArchiveCheckAnnotation] != "enabled" {
		t.Error("the recovered cluster archives on into the same, non-empty archive")
	}
	for _, field := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "finalizers", "managedFields"} {
		if _, has := got.Object["metadata"].(map[string]interface{})[field]; has {
			t.Errorf("server-owned metadata %q must not be sent back", field)
		}
	}
	if _, has := got.Object["status"]; has {
		t.Error("status must not be sent back")
	}
	// The live object is the template on a retry; it must not be changed.
	if _, has := liveSpec["bootstrap"].(map[string]interface{})["recovery"]; has {
		t.Error("the live cluster was modified")
	}
}

func TestBuildInPlaceRecoveryCarriesARestoredProjectsDatabase(t *testing.T) {
	live := liveCluster(t)
	spec := live.Object["spec"].(map[string]interface{})
	spec["bootstrap"] = map[string]interface{}{"recovery": map[string]interface{}{"source": "clusterBackup", "database": "shop", "owner": "owner"}}
	spec["externalClusters"] = []interface{}{recoverySource("dst")}

	got, err := BuildInPlaceRecovery(live, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovery := got.Object["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	if recovery["database"] != "shop" || recovery["owner"] != "owner" {
		t.Errorf("recovery %v", recovery)
	}
	external := got.Object["spec"].(map[string]interface{})["externalClusters"].([]interface{})
	params := external[0].(map[string]interface{})["plugin"].(map[string]interface{})["parameters"].(map[string]interface{})
	if params["barmanObjectName"] != "dst-backups" {
		t.Errorf("a restored project recovers from its own archive, not its source's: %v", params)
	}
}

func TestBuildInPlaceRecoveryDefaultDatabaseNamesNothing(t *testing.T) {
	live := liveCluster(t)
	live.Object["spec"].(map[string]interface{})["bootstrap"] = map[string]interface{}{"initdb": map[string]interface{}{}}
	got, err := BuildInPlaceRecovery(live, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovery := got.Object["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	if _, has := recovery["database"]; has {
		t.Errorf("CNPG's defaults are not named: %v", recovery)
	}
}

func TestBuildInPlaceRecoveryNeedsAStoreToRecoverFrom(t *testing.T) {
	if _, err := BuildInPlaceRecovery(&unstructured.Unstructured{Object: map[string]interface{}{}}, nil); !errors.Is(err, ErrClusterHasNoArchive) {
		t.Fatalf("no spec: %v", err)
	}
	live := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{
		"plugins": []interface{}{
			map[string]interface{}{"name": "other", "isWALArchiver": true},
			map[string]interface{}{"name": BarmanCloudPluginName, "isWALArchiver": true, "parameters": map[string]interface{}{}},
		},
	}}}
	if _, err := BuildInPlaceRecovery(live, nil); !errors.Is(err, ErrClusterHasNoArchive) {
		t.Fatalf("no object store: %v", err)
	}
}

func TestMockKeepSecretsPastOwnerRecordsEachSecret(t *testing.T) {
	mock := NewMockClient()
	mock.KeepSecretsError = errors.New("forbidden")
	if err := mock.KeepSecretsPastOwner(context.Background(), "ns", []string{"a", "b"}); err == nil {
		t.Fatal("the configured error is returned")
	}
	if len(mock.Calls) != 2 || mock.Calls[1] != "KeepSecretsPastOwner:ns/b" {
		t.Errorf("calls %v", mock.Calls)
	}
}

func TestBuildInPlaceRecoveryNeedsAnArchive(t *testing.T) {
	live := liveCluster(t)
	delete(live.Object["spec"].(map[string]interface{}), "plugins")
	if _, err := BuildInPlaceRecovery(live, nil); !errors.Is(err, ErrClusterHasNoArchive) {
		t.Fatalf("got %v", err)
	}
}

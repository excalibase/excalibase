package service

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-522: a DocumentDB project is restored as a DocumentDB project — the
// same cluster a new one gets, recovered from its backups, with credentials
// of its own.

const restoredOwnerPassword = "restored-cluster-password"

func documentDBSource() *domain.DatabaseInstance {
	src := sourceInstance()
	src.DocumentDB = true
	src.Username = "app"
	return src
}

// documentDBRestoreAdapter is a ready adapter whose recovered cluster has its
// own owner credential, as CNPG writes one for every cluster it bootstraps.
func documentDBRestoreAdapter(t *testing.T, mock *k8s.MockClient, reg *fakeRegistrar) *K8sBackupAdapter {
	t.Helper()
	adapter := newRestoreReadyAdapter(t, mock, reg)
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())
	mock.Secrets["org-dst/dst-postgres-app"] = map[string][]byte{
		"username": []byte("app"),
		"password": []byte(restoredOwnerPassword),
	}
	return adapter
}

func restoreDocumentDB(t *testing.T, adapter *K8sBackupAdapter) *domain.ProvisioningResponse {
	t.Helper()
	resp, err := adapter.Restore(context.Background(), documentDBSource(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return resp
}

func restoredClusterSpec(t *testing.T, mock *k8s.MockClient) map[string]interface{} {
	t.Helper()
	obj, ok := mock.CRDs["org-dst/dst-postgres"]
	if !ok {
		t.Fatalf("restored cluster not applied; CRDs=%v", mock.CRDs)
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	return spec
}

// The restored cluster is the cluster creation builds for a DocumentDB
// project — preload, pg_cron, loopback trust, the gateway plugin beside the
// backup plugin — with only its bootstrap replaced by the recovery.
func TestK8sRestoreOfDocumentDBProjectBuildsTheDocumentDBCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	restoreDocumentDB(t, documentDBRestoreAdapter(t, mock, &fakeRegistrar{}))
	spec := restoredClusterSpec(t, mock)

	image, err := config.PostgresImage("17")
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	created := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "dst", Namespace: "org-dst", Tier: enterprisePlan().plan.Config, ImageName: image,
		MasterUsername: "app", DocumentDB: true, DocumentDBGatewayImage: config.DocumentDBGatewayImage(),
	})
	want, _, _ := unstructured.NestedMap(created.Object, "spec", "postgresql")
	if !reflect.DeepEqual(spec["postgresql"], want) {
		t.Errorf("postgresql section differs from creation:\n got %v\nwant %v", spec["postgresql"], want)
	}
	for _, key := range []string{"stopDelay", "smartShutdownTimeout", "certificates"} {
		if !reflect.DeepEqual(spec[key], created.Object["spec"].(map[string]interface{})[key]) {
			t.Errorf("%s differs from creation: got %v", key, spec[key])
		}
	}
	plugins := pluginNames(spec)
	if !slices.Contains(plugins, config.DocumentDBPluginName) || !slices.Contains(plugins, k8s.BarmanCloudPluginName) {
		t.Errorf("a restored DocumentDB cluster names both plugins, got %v", plugins)
	}
	bootstrap, _ := spec["bootstrap"].(map[string]interface{})
	if _, ok := bootstrap["recovery"]; !ok {
		t.Errorf("the cluster must be bootstrapped by recovery: %v", bootstrap)
	}
	if _, ok := bootstrap["initdb"]; ok {
		t.Errorf("a recovered cluster must not run initdb: %v", bootstrap)
	}
}

func pluginNames(spec map[string]interface{}) []string {
	var names []string
	plugins, _ := spec["plugins"].([]interface{})
	for _, plugin := range plugins {
		if entry, ok := plugin.(map[string]interface{}); ok {
			names = append(names, entry["name"].(string))
		}
	}
	return names
}

// The gateway's env reads this Secret; it must exist, empty, before the
// cluster whose pod references it.
func TestK8sRestoreOfDocumentDBProjectWritesTheGatewaySecretBeforeTheCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	restoreDocumentDB(t, documentDBRestoreAdapter(t, mock, &fakeRegistrar{}))

	name := k8s.DocumentDBCredentialSecretName("dst")
	secret, ok := mock.Secrets["org-dst/"+name]
	if !ok {
		t.Fatalf("gateway credential Secret %s not written", name)
	}
	if len(secret["username"]) != 0 || len(secret["password"]) != 0 {
		t.Errorf("the gateway Secret must be empty so the gateway mints no identity: %q", secret)
	}
	secretAt := slices.Index(mock.Calls, "CreateSecret:org-dst/"+name)
	clusterAt := slices.Index(mock.Calls, "ApplyCRD:org-dst/dst-postgres")
	if secretAt < 0 || clusterAt < 0 || secretAt > clusterAt {
		t.Errorf("secret at %d, cluster at %d: %v", secretAt, clusterAt, mock.Calls)
	}
}

func TestK8sRestoreOfDocumentDBProjectExposesTheGateway(t *testing.T) {
	mock := k8s.NewMockClient()
	restoreDocumentDB(t, documentDBRestoreAdapter(t, mock, &fakeRegistrar{}))

	if !mock.DocumentDBServices["org-dst/dst"] {
		t.Errorf("gateway Service not created: %v", mock.DocumentDBServices)
	}
}

// The restored project is recorded as DocumentDB, and its owner credential is
// the recovered cluster's own, forced onto the owner role so the source's
// password opens nothing.
func TestK8sRestoreOfDocumentDBProjectRegistersItWithNewCredentials(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	restoreDocumentDB(t, documentDBRestoreAdapter(t, mock, reg))

	if len(reg.calls) != 1 {
		t.Fatalf("registrations: %d", len(reg.calls))
	}
	restored, opts := reg.calls[0], reg.opts[0]
	if !restored.DocumentDB {
		t.Error("the restored project must be recorded as DocumentDB")
	}
	if restored.Password != restoredOwnerPassword || restored.Password == "source-owner-password" {
		t.Errorf("owner password: got %q, want the recovered cluster's own", restored.Password)
	}
	if !opts.ResetRolePasswords || !opts.ResetAdminPassword {
		t.Errorf("every role, the owner included, must get a new password: %+v", opts)
	}
}

// Without a credential of its own the restore would have to hand out the
// source's, which still opens the source; it is refused instead.
func TestK8sRestoreOfDocumentDBProjectRefusesTheSourcesCredential(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())

	_, err := adapter.Restore(context.Background(), documentDBSource(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want the restore refused", err)
	}
	if !slices.Contains(mock.Calls, "ApplyCRD:org-dst/dst-postgres") {
		t.Fatalf("the refusal must come from the missing credential, not earlier: %v", mock.Calls)
	}
	if len(reg.calls) != 0 {
		t.Errorf("nothing may be registered: %v", reg.calls)
	}
	if mock.Namespaces["org-dst"] {
		t.Errorf("the restore's namespace must be compensated away: %v", mock.Namespaces)
	}
}

// A source whose major cannot carry DocumentDB is refused before anything
// exists, rather than recovered into a cluster that cannot load it.
func TestK8sRestoreOfDocumentDBProjectRefusesAMajorWithoutDocumentDB(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := documentDBRestoreAdapter(t, mock, &fakeRegistrar{})
	src := documentDBSource()
	src.PostgresVersion = "14"

	_, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrDocumentDBNotAvailableOnMajor) {
		t.Fatalf("err: got %v, want ErrDocumentDBNotAvailableOnMajor", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

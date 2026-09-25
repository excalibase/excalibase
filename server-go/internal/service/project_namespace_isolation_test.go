package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeKube is a real k8s.Client over in-memory API fakes, so a test sees the
// exact objects a namespace creation leaves behind.
func fakeKube() (*k8s.Client, kubernetes.Interface) {
	clientset := fake.NewSimpleClientset()
	return k8s.NewClientFromInterfaces(clientset, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())), clientset
}

// assertProjectNamespaceIsolated checks what a fresh project's namespace
// carries: the org label, the default-deny ingress policy and the quota.
func assertProjectNamespaceIsolated(t *testing.T, clientset kubernetes.Interface, namespace, orgID string) {
	t.Helper()
	ctx := context.Background()
	ns, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace %s not created: %v", namespace, err)
	}
	if ns.Labels["excalibase.io/type"] != "project" || ns.Labels["excalibase.io/org"] != orgID {
		t.Errorf("namespace labels = %v, want project/%s", ns.Labels, orgID)
	}
	policy, err := clientset.NetworkingV1().NetworkPolicies(namespace).Get(ctx, "namespace-isolation", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("default-deny isolation policy missing in %s: %v", namespace, err)
	}
	if len(policy.Spec.PodSelector.MatchLabels) != 0 || len(policy.Spec.Ingress) != 1 {
		t.Errorf("isolation policy must select every pod with one allow rule, got %+v", policy.Spec)
	}
	quota, err := clientset.CoreV1().ResourceQuotas(namespace).Get(ctx, "namespace-quota", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("resource quota missing in %s: %v", namespace, err)
	}
	if pods := quota.Spec.Hard["pods"]; pods.Value() != 20 {
		t.Errorf("pods quota = %d, want 20", pods.Value())
	}
}

func TestRestoreNamespaceIsIsolatedAndQuotaed(t *testing.T) {
	client, clientset := fakeKube()
	adapter := NewK8sBackupAdapter(client, t.TempDir(), StaticBackupStorage(r2Storage()))
	pc := provisioner.NewProvisionContext(nil, nil)
	req := domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}

	if err := adapter.createRestoreCluster(context.Background(), pc, sourceInstance(), req, r2Storage(), "org-dst"); err != nil {
		t.Fatalf("createRestoreCluster: %v", err)
	}
	assertProjectNamespaceIsolated(t, clientset, "org-dst", "org")
}

func TestRestoreCreatesNamespaceOnlyAsAProjectNamespace(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertOnlyProjectNamespaceCalls(t, mock, "CreateProjectNamespace:org-dst")
	if mock.NamespaceLabels["org-dst"]["excalibase.io/org"] != "org" {
		t.Errorf("restored namespace must carry the source org, labels %v", mock.NamespaceLabels["org-dst"])
	}
}

func TestRestoreRefusesSourceWithoutOrg(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	src := sourceInstance()
	src.OrgID = ""

	_, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, k8s.ErrProjectOrgRequired) {
		t.Fatalf("err = %v, want ErrProjectOrgRequired", err)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("nothing may be created for a source without an org: %v", mock.Calls)
	}
}

func TestCloneNamespaceIsIsolatedAndQuotaed(t *testing.T) {
	client, clientset := fakeKube()
	svc := newCloneService(t, client, "org1")

	resp, err := svc.CloneDatabase(context.Background(), testOpsDB, domain.CloneRequest{NewProjectName: "clone"})
	if err != nil {
		t.Fatalf("CloneDatabase: %v", err)
	}
	assertProjectNamespaceIsolated(t, clientset, resp.Namespace, "org1")
}

func TestCloneRefusesSourceWithoutOrg(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := newCloneService(t, mock, "")

	_, err := svc.CloneDatabase(context.Background(), testOpsDB, domain.CloneRequest{NewProjectName: "clone"})
	if !errors.Is(err, k8s.ErrProjectOrgRequired) {
		t.Fatalf("err = %v, want ErrProjectOrgRequired", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created for a source without an org: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

func newCloneService(t *testing.T, client k8s.KubeClient, orgID string) *ProvisioningService {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: testOpsDB, OrgID: orgID, Namespace: testOpsDBNS,
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE",
		Host: "h.local", DatabaseName: "app",
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	factory := provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(client, ""))
	return NewProvisioningService(store, factory, client)
}

// assertOnlyProjectNamespaceCalls fails on any namespace creation that did not
// go through the project path.
func assertOnlyProjectNamespaceCalls(t *testing.T, mock *k8s.MockClient, want string) {
	t.Helper()
	found := false
	for _, call := range mock.Calls {
		switch {
		case call == want:
			found = true
		case strings.HasPrefix(call, "CreateNamespace"):
			t.Errorf("namespace created outside the project path: %s", call)
		}
	}
	if !found {
		t.Errorf("expected %s in %v", want, mock.Calls)
	}
}

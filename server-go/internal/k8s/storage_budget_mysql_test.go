package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func mysqlBudgetClient(objects ...runtime.Object) (*Client, *dynamicfake.FakeDynamicClient) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			CNPGClusterGVR: "ClusterList",
			MOCOClusterGVR: "MySQLClusterList",
		})
	return NewClientFromInterfaces(fake.NewSimpleClientset(objects...), dyn).WithMySQL(true), dyn
}

// refuseMySQLList makes every MySQLCluster list fail as the API server would.
func refuseMySQLList(dyn *dynamicfake.FakeDynamicClient, err error) {
	dyn.PrependReactor("list", "mysqlclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

var errMySQLForbidden = apierrors.NewForbidden(schema.GroupResource{Group: "moco.cybozu.com", Resource: "mysqlclusters"}, "",
	errors.New(`User "system:serviceaccount:excalibase-platform:provisioning" cannot list resource "mysqlclusters"`))

// v1.6.0 incident: with MySQL off the chart grants no MOCO rights (and MOCO may
// not be installed), so the budget must not ask for MySQL clusters at all.
func TestStorageAllocated_MySQLOffNeverListsMySQLClusters(t *testing.T) {
	c, _, dyn := budgetFakeClient(projectNamespace("org-p1"),
		budgetClaim("org-p1", "p1-postgres-1", "5Gi", "5Gi", map[string]string{"cnpg.io/cluster": "p1-postgres"}))
	refuseMySQLList(dyn, errMySQLForbidden)
	ctx := context.Background()
	if _, err := dyn.Resource(CNPGClusterGVR).Namespace("org-p1").Create(ctx, cnpgCluster("org-p1", "p1-postgres", 2, "5Gi"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.StorageAllocated(ctx)
	if err != nil {
		t.Fatalf("StorageAllocated with MySQL off: %v", err)
	}
	gi := int64(1) << 30
	if got.TenantBytes != 5*gi || got.PendingBytes != 5*gi {
		t.Fatalf("allocation = %+v", got)
	}
}

// With MySQL on, a MySQL cluster list that fails, for any reason, fails the budget.
func TestStorageAllocated_MySQLOnFailsLoudly(t *testing.T) {
	missingCRD := apierrors.NewNotFound(schema.GroupResource{Group: "moco.cybozu.com", Resource: "mysqlclusters"}, "")
	for name, refusal := range map[string]error{"forbidden": errMySQLForbidden, "CRD missing": missingCRD} {
		t.Run(name, func(t *testing.T) {
			c, dyn := mysqlBudgetClient(projectNamespace("org-p1"))
			refuseMySQLList(dyn, refusal)
			if _, err := c.StorageAllocated(context.Background()); err == nil {
				t.Fatal("the budget read with MySQL on must fail when MySQL clusters cannot be listed")
			}
		})
	}
}

// ADR 0041: a MySQL cluster's volumes count against the storage budget like a
// Postgres cluster's, both made and still to be made.
func TestStorageAllocated_CountsMySQLClustersInFlight(t *testing.T) {
	mocoLabels := map[string]string{"app.kubernetes.io/created-by": "moco", "app.kubernetes.io/name": "mysql",
		"app.kubernetes.io/instance": "proj-abc-mysql"}
	c, dyn := mysqlBudgetClient(projectNamespace("org-proj-abc"),
		budgetClaim("org-proj-abc", "mysql-data-moco-proj-abc-mysql-0", "50Gi", "50Gi", mocoLabels))
	cluster, err := BuildMySQLCluster(mysqlOpts(standardPlan))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := dyn.Resource(MOCOClusterGVR).Namespace("org-proj-abc").Create(ctx, cluster, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.StorageAllocated(ctx)
	if err != nil {
		t.Fatalf("StorageAllocated: %v", err)
	}
	gi := int64(1) << 30
	if got.TenantBytes != 50*gi || got.PendingBytes != 100*gi {
		t.Fatalf("allocation = %+v", got)
	}
}

func TestApplyCRDReservesAMySQLClustersVolumes(t *testing.T) {
	c, dyn := mysqlBudgetClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "org-proj-abc"}})
	reserver := &recordingReserver{room: 100 * (1 << 30)}
	c.SetStorageReserver(reserver)
	ctx := context.Background()
	standard, _ := BuildMySQLCluster(mysqlOpts(standardPlan))
	if err := c.ApplyCRD(ctx, MOCOClusterGVR, "org-proj-abc", standard); !errors.Is(err, errNoRoom) {
		t.Fatalf("err = %v, want the 150Gi reservation refused", err)
	}
	if _, err := dyn.Resource(MOCOClusterGVR).Namespace("org-proj-abc").Get(ctx, "proj-abc-mysql", metav1.GetOptions{}); err == nil {
		t.Fatal("a refused cluster was created")
	}
	free, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	if err := c.ApplyCRD(ctx, MOCOClusterGVR, "org-proj-abc", free); err != nil {
		t.Fatalf("a 5Gi cluster within the room: %v", err)
	}
	gi := int64(1) << 30
	if len(reserver.asked) != 2 || reserver.asked[1] != 5*gi {
		t.Fatalf("asked = %v", reserver.asked)
	}
}

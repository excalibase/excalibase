package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var testAccess = ProjectAccess{
	ClusterRole:    "excalibase-provisioning-project",
	ServiceAccount: "provisioning-sa",
	Namespace:      "excalibase-platform",
}

// allowAfter makes SelfSubjectAccessReviews answer allowed from the n-th call on.
func allowAfter(clientset *fake.Clientset, n int) *int {
	calls := 0
	clientset.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview).DeepCopy()
		review.Status.Allowed = calls >= n
		return true, review, nil
	})
	return &calls
}

func newAccessClient(t *testing.T, allowedFrom int) (*Client, *fake.Clientset, *int) {
	t.Helper()
	clientset := fake.NewSimpleClientset()
	calls := allowAfter(clientset, allowedFrom)
	client := NewClientFromInterfaces(clientset, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())).
		WithProjectAccess(testAccess)
	client.accessPoll = time.Millisecond
	client.accessTimeout = 200 * time.Millisecond
	return client, clientset, calls
}

// Provisioning holds its tenant permissions only inside project namespaces: a
// RoleBinding in each, made as the namespace is created.
func TestCreateProjectNamespace_BindsProvisioningInsideTheNamespace(t *testing.T) {
	client, clientset, _ := newAccessClient(t, 1)
	if err := client.CreateProjectNamespace(context.Background(), testNS, "org"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}
	binding, err := clientset.RbacV1().RoleBindings(testNS).Get(context.Background(), ProjectRoleBindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no RoleBinding in the project namespace: %v", err)
	}
	wantRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: testAccess.ClusterRole}
	if binding.RoleRef != wantRef {
		t.Errorf("roleRef %+v, want %+v", binding.RoleRef, wantRef)
	}
	wantSubjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: testAccess.ServiceAccount, Namespace: testAccess.Namespace}}
	if len(binding.Subjects) != 1 || binding.Subjects[0] != wantSubjects[0] {
		t.Errorf("subjects %+v, want %+v", binding.Subjects, wantSubjects)
	}
}

// Everything after the namespace (policies, quota) is done with the binding's
// permissions, so the namespace is not handed back until they are in force.
func TestCreateProjectNamespace_WaitsUntilTheBindingIsInForce(t *testing.T) {
	client, clientset, calls := newAccessClient(t, 3)
	if err := client.CreateProjectNamespace(context.Background(), testNS, "org"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}
	if *calls < 3 {
		t.Errorf("checked access %d times, want it to wait until allowed", *calls)
	}
	if _, err := clientset.NetworkingV1().NetworkPolicies(testNS).Get(context.Background(), namespaceDefaultDenyPolicy, metav1.GetOptions{}); err != nil {
		t.Errorf("isolation policy not applied after the binding took effect: %v", err)
	}
}

func TestCreateProjectNamespace_FailsWhenTheBindingNeverTakesEffect(t *testing.T) {
	client, _, _ := newAccessClient(t, 1<<30)
	err := client.CreateProjectNamespace(context.Background(), testNS, "org")
	if !errors.Is(err, ErrProjectAccessNotInForce) {
		t.Fatalf("want ErrProjectAccessNotInForce, got %v", err)
	}
}

// No configured identity is not a reason to skip the binding: the namespace
// would be one provisioning cannot manage.
func TestCreateProjectNamespace_RefusesWithoutProjectAccess(t *testing.T) {
	for name, access := range map[string]ProjectAccess{
		"nothing":      {},
		"no role":      {ServiceAccount: "provisioning-sa", Namespace: "excalibase-platform"},
		"no account":   {ClusterRole: "r", Namespace: "excalibase-platform"},
		"no namespace": {ClusterRole: "r", ServiceAccount: "provisioning-sa"},
	} {
		t.Run(name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			client := NewClientFromInterfaces(clientset, nil).WithProjectAccess(access)
			err := client.CreateProjectNamespace(context.Background(), testNS, "org")
			if !errors.Is(err, ErrProjectAccessUnset) {
				t.Fatalf("want ErrProjectAccessUnset, got %v", err)
			}
			if _, getErr := clientset.CoreV1().Namespaces().Get(context.Background(), testNS, metav1.GetOptions{}); getErr == nil {
				t.Error("created the namespace anyway")
			}
		})
	}
}

// A binding left by an earlier attempt is reused only if it grants exactly this.
func TestCreateProjectNamespace_RefusesAForeignBinding(t *testing.T) {
	client, clientset, _ := newAccessClient(t, 1)
	clientset.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		foreign := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: ProjectRoleBindingName, Namespace: testNS},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		}
		if err := clientset.Tracker().Add(foreign); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
		return false, nil, nil
	})
	err := client.CreateProjectNamespace(context.Background(), testNS, "org")
	if !errors.Is(err, ErrForeignProjectBinding) {
		t.Fatalf("want ErrForeignProjectBinding, got %v", err)
	}
}

func TestWithProjectAccess_LeavesTheOriginalUnchanged(t *testing.T) {
	original := NewClientFromInterfaces(fake.NewSimpleClientset(), nil)
	configured := original.WithProjectAccess(testAccess)
	if original.projectAccess != (ProjectAccess{}) {
		t.Error("WithProjectAccess changed the receiver")
	}
	if configured.projectAccess != testAccess {
		t.Errorf("configured access %+v", configured.projectAccess)
	}
}

// Once a namespace is gone or terminating, provisioning's binding in it is gone
// or going too.
func TestNamespaceDeleting(t *testing.T) {
	now := metav1.Now()
	client := newFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "live"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "going", DeletionTimestamp: &now, Finalizers: []string{"kubernetes"}}},
	)
	for name, want := range map[string]bool{"live": false, "going": true, "gone": true} {
		got, err := client.NamespaceDeleting(context.Background(), name)
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v; want %v", name, got, err, want)
		}
	}
}

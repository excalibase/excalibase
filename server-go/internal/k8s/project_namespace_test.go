package k8s

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateProjectNamespace_LabelsTheTenantOrg(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.CreateProjectNamespace(ctx, "org-1-proj", "org-1"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, "org-1-proj", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace lookup: %v", err)
	}
	want := map[string]string{"excalibase.io/type": "project", "excalibase.io/org": "org-1"}
	for key, value := range want {
		if ns.Labels[key] != value {
			t.Errorf("label %s = %q, want %q (labels %v)", key, ns.Labels[key], value, ns.Labels)
		}
	}
}

func TestCreateProjectNamespace_RefusesMissingOrg(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	err := c.CreateProjectNamespace(ctx, "-proj", "")
	if !errors.Is(err, ErrProjectOrgRequired) {
		t.Fatalf("err = %v, want ErrProjectOrgRequired", err)
	}
	list, _ := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("no namespace may be created without an org, got %v", list.Items)
	}
}

func TestProjectNamespaceLabels_RefusesMissingOrg(t *testing.T) {
	if _, err := ProjectNamespaceLabels(""); !errors.Is(err, ErrProjectOrgRequired) {
		t.Errorf("err = %v, want ErrProjectOrgRequired", err)
	}
}

func TestMockCreateProjectNamespace_RecordsLabels(t *testing.T) {
	m := NewMockClient()
	if err := m.CreateProjectNamespace(context.Background(), "org-1-proj", "org-1"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}
	if !m.Namespaces["org-1-proj"] {
		t.Error("namespace not recorded")
	}
	if m.NamespaceLabels["org-1-proj"]["excalibase.io/org"] != "org-1" {
		t.Errorf("labels: %v", m.NamespaceLabels["org-1-proj"])
	}
}

func TestMockCreateProjectNamespace_RefusesMissingOrg(t *testing.T) {
	m := NewMockClient()
	err := m.CreateProjectNamespace(context.Background(), "-proj", "")
	if !errors.Is(err, ErrProjectOrgRequired) {
		t.Fatalf("err = %v, want ErrProjectOrgRequired", err)
	}
	if len(m.Namespaces) != 0 {
		t.Errorf("no namespace may be recorded without an org: %v", m.Namespaces)
	}
}

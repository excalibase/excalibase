package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func labelledNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// EXC-659: the log collector copies this label onto a project's log lines.
func TestProjectNamespacePlans_ReadsOnlyProjectNamespaces(t *testing.T) {
	c := newFakeClient(
		labelledNamespace("org-proj-a", map[string]string{"excalibase.io/type": "project", NamespacePlanLabel: "free"}),
		labelledNamespace("org-proj-b", map[string]string{"excalibase.io/type": "project"}),
		labelledNamespace("kube-system", map[string]string{NamespacePlanLabel: "standard"}),
	)
	got, err := c.ProjectNamespacePlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["org-proj-a"] != "free" || got["org-proj-b"] != "" {
		t.Errorf("plans %v", got)
	}
}

func TestSetNamespacePlan_ChangesOnlyThePlanLabel(t *testing.T) {
	labels := map[string]string{"excalibase.io/type": "project", "excalibase.io/org": "org-1", NamespacePlanLabel: "free"}
	c := newFakeClient(labelledNamespace("org-proj-a", labels))
	ctx := context.Background()
	if err := c.SetNamespacePlan(ctx, "org-proj-a", "standard"); err != nil {
		t.Fatal(err)
	}
	ns, _ := c.clientset.CoreV1().Namespaces().Get(ctx, "org-proj-a", metav1.GetOptions{})
	if ns.Labels[NamespacePlanLabel] != "standard" || ns.Labels["excalibase.io/org"] != "org-1" || ns.Labels["excalibase.io/type"] != "project" {
		t.Errorf("labels %v", ns.Labels)
	}
}

func TestSetNamespacePlan_RefusesAnythingButAPlan(t *testing.T) {
	c := newFakeClient(labelledNamespace("org-proj-a", map[string]string{"excalibase.io/type": "project"}))
	for _, plan := range []string{"", "FREE", "pro", "free,x"} {
		if err := c.SetNamespacePlan(context.Background(), "org-proj-a", plan); err == nil {
			t.Errorf("plan %q accepted", plan)
		}
	}
}

func TestSetNamespacePlan_ReportsAMissingNamespace(t *testing.T) {
	if err := newFakeClient().SetNamespacePlan(context.Background(), "gone", "free"); err == nil {
		t.Fatal("labelling a missing namespace reported success")
	}
}

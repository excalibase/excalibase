package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestProjectNetworkPolicyLeavesAppsToTheirEdgeFence(t *testing.T) {
	store := emptyInstanceStore(t)
	mock := k8s.NewMockClient()
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "np-db", OrgID: "org1", Namespace: "org1-np-db", Status: "ACTIVE", DBType: domain.PostgreSQL}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewNetworkPolicyService(store, mock).UpdateNetworkPolicy(context.Background(), "np-db",
		domain.NetworkConfig{PolicyEnabled: true, AllowedCIDRs: []string{"203.0.113.0/24"}}); err != nil {
		t.Fatalf("UpdateNetworkPolicy: %v", err)
	}

	raw, _, err := unstructured.NestedMap(mock.CRDs["org1-np-db/np-db-network-policy"].Object, "spec", "podSelector")
	if err != nil {
		t.Fatalf("podSelector: %v", err)
	}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); err != nil {
		t.Fatalf("decode podSelector: %v", err)
	}
	compiled, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		t.Fatalf("compile podSelector: %v", err)
	}
	if compiled.Matches(labels.Set{"excalibase.io/component": "app", "excalibase.io/app": "a1"}) {
		t.Error("a project-wide allow must not select app pods")
	}
	if !compiled.Matches(labels.Set{"cnpg.io/cluster": "np-db-postgres"}) {
		t.Error("the allow must still select the project's database")
	}
}

package k8s

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

func unstructuredYAML(t *testing.T, obj *unstructured.Unstructured) string {
	t.Helper()
	encoded, err := yaml.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "---\n" + string(encoded)
}

func privateNetworkSpec(t *testing.T, policy *unstructured.Unstructured) ciliumPolicySpec {
	t.Helper()
	var spec ciliumPolicySpec
	content, _, _ := unstructured.NestedMap(policy.Object, "spec")
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	return spec
}

func TestAppPrivateNetworkPolicySelectsOnlyThisNamespacesApps(t *testing.T) {
	policy, err := buildAppPrivateNetworkPolicy(testNamespace)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if policy.GetName() != AppPrivateNetworkPolicyName || policy.GetNamespace() != testNamespace {
		t.Fatalf("policy is %s/%s", policy.GetNamespace(), policy.GetName())
	}
	if _, ok := policy.GetLabels()["excalibase.io/app"]; ok {
		t.Error("the policy must not carry an app's id, or deleting that app would delete the project's setting")
	}
	spec := privateNetworkSpec(t, policy)
	wantApps := map[string]string{componentLabelKey: appComponentLabel}
	if !maps.Equal(spec.EndpointSelector.MatchLabels, wantApps) {
		t.Errorf("endpoint selector = %v, want only app pods", spec.EndpointSelector.MatchLabels)
	}
	peer := metav1.LabelSelector{MatchLabels: map[string]string{componentLabelKey: appComponentLabel, podNamespaceKey: testNamespace}}
	if len(spec.Ingress) != 1 || !reflect.DeepEqual(spec.Ingress[0].FromEndpoints, []metav1.LabelSelector{peer}) {
		t.Fatalf("ingress = %+v, want one rule from this namespace's apps only", spec.Ingress)
	}
	if len(spec.Ingress[0].FromEntities) != 0 {
		t.Errorf("no entity may be admitted: %v", spec.Ingress[0].FromEntities)
	}
	if len(spec.Egress) != 1 || !reflect.DeepEqual(spec.Egress[0].ToEndpoints, []metav1.LabelSelector{peer}) {
		t.Fatalf("egress = %+v, want one rule to this namespace's apps only", spec.Egress)
	}
	if len(spec.Egress[0].ToEntities) != 0 || len(spec.Egress[0].ToCIDRSet) != 0 {
		t.Errorf("egress must name app endpoints only: %+v", spec.Egress[0])
	}
}

// The ports are named, so one static policy admits each app's own HTTP port
// without being re-rendered per app.
func TestAppPrivateNetworkPolicyAdmitsTheNamedHTTPPortOnly(t *testing.T) {
	policy, err := buildAppPrivateNetworkPolicy(testNamespace)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	spec := privateNetworkSpec(t, policy)
	want := []ciliumPortRule{{Ports: []ciliumPort{{Port: appServicePortName, Protocol: protocolTCP}}}}
	if !reflect.DeepEqual(spec.Ingress[0].ToPorts, want) {
		t.Errorf("ingress ports = %+v, want %+v", spec.Ingress[0].ToPorts, want)
	}
}

func TestAppPrivateNetworkPolicyRefusesABadNamespace(t *testing.T) {
	if _, err := buildAppPrivateNetworkPolicy("Not A Namespace"); !errors.Is(err, ErrRenderApp) {
		t.Fatalf("err = %v, want ErrRenderApp", err)
	}
}

func TestAppPrivateNetworkGolden(t *testing.T) {
	policy, err := buildAppPrivateNetworkPolicy("org1-proj")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	assertGoldenAt(t, "testdata/namespace_policies/app-private-network.yaml", unstructuredYAML(t, policy))
}

func TestSetAppPrivateNetwork_OpensThenCloses(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if open, err := c.AppPrivateNetworkOpen(ctx, testNamespace); err != nil || open {
		t.Fatalf("before: open=%v err=%v, want closed", open, err)
	}
	if err := c.SetAppPrivateNetwork(ctx, testNamespace, true); err != nil {
		t.Fatalf("open: %v", err)
	}
	if open, err := c.AppPrivateNetworkOpen(ctx, testNamespace); err != nil || !open {
		t.Fatalf("after open: open=%v err=%v", open, err)
	}
	// Applying twice converges instead of failing on the existing object.
	if err := c.SetAppPrivateNetwork(ctx, testNamespace, true); err != nil {
		t.Fatalf("open again: %v", err)
	}
	if err := c.SetAppPrivateNetwork(ctx, testNamespace, false); err != nil {
		t.Fatalf("close: %v", err)
	}
	if open, err := c.AppPrivateNetworkOpen(ctx, testNamespace); err != nil || open {
		t.Fatalf("after close: open=%v err=%v", open, err)
	}
	if err := c.SetAppPrivateNetwork(ctx, testNamespace, false); err != nil {
		t.Fatalf("closing an absent policy is a success: %v", err)
	}
}

func TestSetAppPrivateNetwork_ErrorsPropagate(t *testing.T) {
	for _, verb := range []string{"get", "create", "delete"} {
		t.Run(verb, func(t *testing.T) {
			c := newFakeClient()
			ctx := context.Background()
			if verb == "delete" {
				if err := c.SetAppPrivateNetwork(ctx, testNamespace, true); err != nil {
					t.Fatalf("open: %v", err)
				}
			}
			c.dynamicClient.(interface {
				PrependReactor(string, string, ktesting.ReactionFunc)
			}).PrependReactor(verb, "ciliumnetworkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New(verb + " refused")
			})
			if err := c.SetAppPrivateNetwork(ctx, testNamespace, verb != "delete"); err == nil {
				t.Fatalf("a refused %s must fail the change", verb)
			}
		})
	}
}

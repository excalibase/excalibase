package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const ingressProject = "proj-pub1"

func readIngressPolicy(t *testing.T, c *Client) *unstructured.Unstructured {
	t.Helper()
	policy, err := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(testNamespace).
		Get(context.Background(), PublicDBIngressPolicyName(ingressProject), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the ingress policy: %v", err)
	}
	return policy
}

func TestPublicDBIngressOpensOnlyTheDatabasePortsToTheWorld(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsurePublicDBIngressPolicy(context.Background(), testNamespace, ingressProject, []int{5432, 10260}); err != nil {
		t.Fatalf("EnsurePublicDBIngressPolicy: %v", err)
	}

	policy := readIngressPolicy(t, c)
	if policy.GetKind() != ciliumPolicyKind {
		t.Errorf("kind: got %q", policy.GetKind())
	}
	selector, _, _ := unstructured.NestedStringMap(policy.Object, "spec", "endpointSelector", "matchLabels")
	wantSelector := map[string]string{"cnpg.io/cluster": ingressProject + "-postgres", "cnpg.io/podRole": "instance"}
	if !reflect.DeepEqual(selector, wantSelector) {
		t.Errorf("selector: got %v, want %v", selector, wantSelector)
	}
	rules, _, _ := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	if len(rules) != 1 {
		t.Fatalf("ingress rules: %v", rules)
	}
	rule := rules[0].(map[string]interface{})
	if entities, _, _ := unstructured.NestedStringSlice(rule, "fromEntities"); !reflect.DeepEqual(entities, []string{worldEntity}) {
		t.Errorf("from: got %v, want only world", entities)
	}
	if got := ingressPorts(t, rule); !reflect.DeepEqual(got, []string{"5432/TCP", "10260/TCP"}) {
		t.Errorf("ports: got %v", got)
	}
	for _, field := range []string{"egress", "egressDeny", "ingressDeny"} {
		if _, found := policy.Object["spec"].(map[string]interface{})[field]; found {
			t.Errorf("the policy carries %s", field)
		}
	}
}

func ingressPorts(t *testing.T, rule map[string]interface{}) []string {
	t.Helper()
	toPorts, _, _ := unstructured.NestedSlice(rule, "toPorts")
	var got []string
	for _, entry := range toPorts {
		ports, _, _ := unstructured.NestedSlice(entry.(map[string]interface{}), "ports")
		for _, port := range ports {
			p := port.(map[string]interface{})
			got = append(got, p["port"].(string)+"/"+p["protocol"].(string))
		}
	}
	return got
}

func TestPublicDBIngressIsReRenderedAndRemoved(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.EnsurePublicDBIngressPolicy(ctx, testNamespace, ingressProject, []int{5432, 10260}); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := c.EnsurePublicDBIngressPolicy(ctx, testNamespace, ingressProject, []int{5432}); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(readIngressPolicy(t, c).Object, "spec", "ingress")
	if got := ingressPorts(t, rules[0].(map[string]interface{})); !reflect.DeepEqual(got, []string{"5432/TCP"}) {
		t.Errorf("ports after re-render: got %v", got)
	}

	for range 2 {
		if err := c.DeletePublicDBIngressPolicy(ctx, testNamespace, ingressProject); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	_, err := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(testNamespace).
		Get(ctx, PublicDBIngressPolicyName(ingressProject), metav1.GetOptions{})
	if err == nil {
		t.Error("the policy is still there")
	}
}

func TestPublicDBIngressErrorsPropagate(t *testing.T) {
	for verb, call := range map[string]func(*Client) error{
		"create": func(c *Client) error {
			return c.EnsurePublicDBIngressPolicy(context.Background(), testNamespace, ingressProject, []int{5432})
		},
		"delete": func(c *Client) error {
			return c.DeletePublicDBIngressPolicy(context.Background(), testNamespace, ingressProject)
		},
	} {
		t.Run(verb, func(t *testing.T) {
			c := newFakeClient()
			c.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor(verb, "ciliumnetworkpolicies", failReactor("boom"))
			if err := call(c); err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestMockRecordsThePublicDBIngressPolicy(t *testing.T) {
	mock := NewMockClient()
	ctx := context.Background()
	if err := mock.EnsurePublicDBIngressPolicy(ctx, testNamespace, ingressProject, []int{5432}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := mock.PublicDBIngress[testNamespace+"/"+ingressProject]; !reflect.DeepEqual(got, []int{5432}) {
		t.Errorf("recorded ports: %v", got)
	}
	if err := mock.DeletePublicDBIngressPolicy(ctx, testNamespace, ingressProject); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, open := mock.PublicDBIngress[testNamespace+"/"+ingressProject]; open {
		t.Error("the policy was not removed")
	}
	mock.PublicDBIngressError = context.Canceled
	if err := mock.EnsurePublicDBIngressPolicy(ctx, testNamespace, ingressProject, nil); err != context.Canceled {
		t.Errorf("ensure error: %v", err)
	}
	if err := mock.DeletePublicDBIngressPolicy(ctx, testNamespace, ingressProject); err != context.Canceled {
		t.Errorf("delete error: %v", err)
	}
}

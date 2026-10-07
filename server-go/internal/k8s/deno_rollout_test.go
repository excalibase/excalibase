package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func denoRuntimeWithStatus(generation int64, status appsv1.DeploymentStatus) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: denoRuntimeName, Namespace: egressNS, Generation: generation},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status:     status,
	}
}

func TestDenoRuntimeRollout(t *testing.T) {
	cases := map[string]struct {
		dep  *appsv1.Deployment
		want DenoRolloutState
	}{
		"no runtime": {nil, DenoRuntimeAbsent},
		"spec not yet seen by the controller": {denoRuntimeWithStatus(2, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
		}), DenoRuntimeRollingOut},
		"new pod not ready, old one still serving": {denoRuntimeWithStatus(2, appsv1.DeploymentStatus{
			ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1,
		}), DenoRuntimeRollingOut},
		"first pod still starting": {denoRuntimeWithStatus(1, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 0,
		}), DenoRuntimeRollingOut},
		"rolled out": {denoRuntimeWithStatus(2, appsv1.DeploymentStatus{
			ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
		}), DenoRuntimeReady},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newFakeClient()
			if tc.dep != nil {
				c = newFakeClient(tc.dep)
			}
			got, err := c.DenoRuntimeRollout(context.Background(), egressNS)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("state = %v, want %v", got, tc.want)
			}
		})
	}
}

// The fence changes before the pod rolls, so the new pod never starts under
// the old allowlist's fence (EXC-569).
func TestEnsureDenoRuntime_FenceChangesBeforeThePodRolls(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", AllowedHosts: []string{"a.example.com"}}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	c.clientset.(*fake.Clientset).ClearActions()

	spec.AllowedHosts = []string{"b.example.com:9443"}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	policyAt, deploymentAt := -1, -1
	for i, action := range c.clientset.(*fake.Clientset).Actions() {
		if action.GetVerb() != "update" {
			continue
		}
		switch action.GetResource().Resource {
		case "networkpolicies":
			policyAt = i
		case "deployments":
			deploymentAt = i
		}
	}
	if policyAt < 0 || deploymentAt < 0 {
		t.Fatalf("want a policy and a deployment update, got policy=%d deployment=%d", policyAt, deploymentAt)
	}
	if policyAt > deploymentAt {
		t.Fatalf("the deployment rolled (action %d) before the fence changed (action %d)", deploymentAt, policyAt)
	}
}

package k8s

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDenoRolloutState_String(t *testing.T) {
	for state, want := range map[DenoRolloutState]string{
		DenoRuntimeAbsent: "absent", DenoRuntimeRollingOut: "rolling out", DenoRuntimeReady: "ready",
	} {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", state, got, want)
		}
	}
}

func TestDenoRuntimeRollout_ReadFailureIsAnError(t *testing.T) {
	c := newFakeClient()
	c.clientset.(*fake.Clientset).PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	if _, err := c.DenoRuntimeRollout(context.Background(), egressNS); err == nil {
		t.Fatal("want the read failure")
	}
}

func TestMockDenoRuntimeRollout(t *testing.T) {
	ctx := context.Background()
	mock := NewMockClient()
	if state, _ := mock.DenoRuntimeRollout(ctx, "ns"); state != DenoRuntimeAbsent {
		t.Fatalf("before ensure: %v", state)
	}
	if err := mock.EnsureDenoRuntime(ctx, "ns", DenoRuntimeSpec{}); err != nil {
		t.Fatal(err)
	}
	if state, _ := mock.DenoRuntimeRollout(ctx, "ns"); state != DenoRuntimeReady {
		t.Fatalf("after ensure: %v", state)
	}
	boom := errors.New("boom")
	mock.DenoRolloutFunc = func(string) (DenoRolloutState, error) { return DenoRuntimeRollingOut, boom }
	if state, err := mock.DenoRuntimeRollout(ctx, "ns"); state != DenoRuntimeRollingOut || !errors.Is(err, boom) {
		t.Fatalf("scripted: %v %v", state, err)
	}
}

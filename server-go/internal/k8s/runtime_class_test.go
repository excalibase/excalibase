package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRuntimeClassExists_FoundAndMissing(t *testing.T) {
	c := newFakeClient(&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"})
	ctx := context.Background()

	if found, err := c.RuntimeClassExists(ctx, "gvisor"); err != nil || !found {
		t.Fatalf("gvisor: got (%v, %v), want (true, nil)", found, err)
	}
	if found, err := c.RuntimeClassExists(ctx, "kata"); err != nil || found {
		t.Fatalf("kata: got (%v, %v), want (false, nil)", found, err)
	}
}

func TestRuntimeClassExists_PropagatesReadError(t *testing.T) {
	c := newFakeClient()
	c.clientset.(*fake.Clientset).PrependReactor("get", "runtimeclasses", failReactor("forbidden"))

	_, err := c.RuntimeClassExists(context.Background(), "gvisor")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("want the read error, got %v", err)
	}
}

func TestMockClient_RuntimeClassExists(t *testing.T) {
	m := NewMockClient()
	m.RuntimeClasses["gvisor"] = true
	ctx := context.Background()

	if found, _ := m.RuntimeClassExists(ctx, "gvisor"); !found {
		t.Error("gvisor should exist")
	}
	if found, _ := m.RuntimeClassExists(ctx, "kata"); found {
		t.Error("kata should not exist")
	}
	m.RuntimeClassError = errors.New("boom")
	if _, err := m.RuntimeClassExists(ctx, "gvisor"); !errors.Is(err, m.RuntimeClassError) {
		t.Errorf("want the scripted error, got %v", err)
	}
}

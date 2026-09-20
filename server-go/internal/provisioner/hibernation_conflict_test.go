package provisioner

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// conflictingClient stands in for the API server while the CNPG operator is
// writing the same Cluster: the first conflicts updates are refused with the
// optimistic-concurrency error a stale resourceVersion earns, and a caller
// that reads the object again gets through.
type conflictingClient struct {
	*k8s.MockClient
	conflicts int
	updates   int
	// gets counts reads, so a test can tell a retry that re-read the object
	// from one that resubmitted the same stale copy.
	gets int
	// ready reports a resumed primary once the flip has landed.
	ready bool
}

func (c *conflictingClient) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := c.MockClient.GetCRD(ctx, gvr, namespace, name)
	if err != nil {
		return nil, err
	}
	c.gets++
	copied := obj.DeepCopy()
	if c.ready {
		_ = unstructured.SetNestedField(copied.Object, int64(1), "status", "readyInstances")
	}
	return copied, nil
}

func (c *conflictingClient) UpdateCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	c.updates++
	if c.updates <= c.conflicts {
		return apierrors.NewConflict(
			schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, obj.GetName(),
			errNotLatest)
	}
	return c.MockClient.UpdateCRD(ctx, gvr, namespace, obj)
}

var errNotLatest = &apierrors.StatusError{ErrStatus: metav1.Status{Message: "the object has been modified"}}

func newConflictingProvisioner(t *testing.T, conflicts int, ready bool) (*conflictingClient, *PostgreSQLProvisioner) {
	t.Helper()
	mock := k8s.NewMockClient()
	cluster := newHibernationTestCluster(3)
	cluster.SetAnnotations(map[string]string{tierInstancesKey: "3"})
	mock.CRDs[pauseTestClusterKey] = cluster
	client := &conflictingClient{MockClient: mock, conflicts: conflicts, ready: ready}
	prov := NewPostgreSQLProvisioner(client, "")
	prov.resumeTimeout = time.Second
	prov.resumePoll = time.Millisecond
	return client, prov
}

// The operator writes the Cluster constantly while it hibernates one, so the
// copy a resume read is routinely stale by the time it is submitted. The flip
// re-reads and tries again rather than reporting a resume that never started.
func TestPostgreSQLResume_RetriesTheFlipTheOperatorRaced(t *testing.T) {
	client, prov := newConflictingProvisioner(t, 1, true)

	if err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if client.updates < 2 {
		t.Fatalf("the refused flip must be retried: updates=%d", client.updates)
	}
	if client.gets < 2 {
		t.Fatalf("a retry must re-read the cluster, not resubmit the stale copy: gets=%d", client.gets)
	}
	assertAnnotation(t, storedCluster(t, client.MockClient), hibernationAnnotation, hibernationOff)
}

// Pause flips the same annotation through the same helper, so it races the
// operator the same way and must converge the same way.
func TestPostgreSQLPause_RetriesTheFlipTheOperatorRaced(t *testing.T) {
	client, prov := newConflictingProvisioner(t, 1, false)

	if err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if client.updates < 2 {
		t.Fatalf("the refused flip must be retried: updates=%d", client.updates)
	}
	assertAnnotation(t, storedCluster(t, client.MockClient), hibernationAnnotation, hibernationOn)
}

// A conflict that never clears is not a resume. The caller is told, and the
// annotation is left as it was — never reported as flipped on the strength of
// a refused write.
func TestPostgreSQLResume_PersistentConflictIsAnError(t *testing.T) {
	client, prov := newConflictingProvisioner(t, 1000, true)

	err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject)
	if err == nil {
		t.Fatal("a flip the API server never accepted must not report a resume")
	}
	if !strings.Contains(err.Error(), "hibernation") {
		t.Fatalf("the error must name the failed flip: %v", err)
	}
	if got := storedCluster(t, client.MockClient).GetAnnotations()[hibernationAnnotation]; got != "" {
		t.Fatalf("nothing may be written: annotation=%q", got)
	}
}

package provisioner

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	pauseTestNS      = "org1-hib"
	pauseTestProject = "hib"
	pauseTestCluster = "hib-postgres"
	tierInstancesKey = "excalibase.io/tier-instances"
	resumeTimeoutMsg = "did not become ready"
)

const pauseTestClusterKey = pauseTestNS + "/" + pauseTestCluster

func newHibernationTestCluster(instances int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]interface{}{
			"name":        pauseTestCluster,
			"namespace":   pauseTestNS,
			"annotations": map[string]interface{}{tierInstancesKey: "3"},
		},
		"spec": map[string]interface{}{"instances": instances},
	}}
}

func setupHibernationTest(t *testing.T) (*k8s.MockClient, *PostgreSQLProvisioner) {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.CRDs[pauseTestClusterKey] = newHibernationTestCluster(3)
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.resumeTimeout = 200 * time.Millisecond
	prov.resumePoll = 5 * time.Millisecond
	return mock, prov
}

func storedCluster(t *testing.T, mock *k8s.MockClient) *unstructured.Unstructured {
	t.Helper()
	obj, ok := mock.CRDs[pauseTestClusterKey]
	if !ok {
		t.Fatal("cluster CRD missing from mock")
	}
	return obj
}

func assertAnnotation(t *testing.T, obj *unstructured.Unstructured, key, want string) {
	t.Helper()
	if got := obj.GetAnnotations()[key]; got != want {
		t.Errorf("annotation %s: got %q, want %q", key, got, want)
	}
}

func assertInstances(t *testing.T, obj *unstructured.Unstructured, want int64) {
	t.Helper()
	got, _, _ := unstructured.NestedInt64(obj.Object, "spec", "instances")
	if got != want {
		t.Errorf("spec.instances: got %d, want %d", got, want)
	}
}

func TestPostgreSQLPause_SetsHibernationOnAndKeepsInstances(t *testing.T) {
	mock, prov := setupHibernationTest(t)

	if err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	cluster := storedCluster(t, mock)
	assertAnnotation(t, cluster, hibernationAnnotation, hibernationOn)
	assertAnnotation(t, cluster, tierInstancesKey, "3")
	assertInstances(t, cluster, 3)
	assertUpdatedNotCreated(t, mock)
}

// A fetched Cluster carries a resourceVersion, and the API server refuses to
// Create such an object. The flip must therefore go through Update, never
// through the create-first ApplyCRD path.
func assertUpdatedNotCreated(t *testing.T, mock *k8s.MockClient) {
	t.Helper()
	updated := false
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "ApplyCRD:") {
			t.Errorf("hibernation flip must not go through ApplyCRD (create-first): %s", call)
		}
		if strings.HasPrefix(call, "UpdateCRD:") {
			updated = true
		}
	}
	if !updated {
		t.Error("expected UpdateCRD call")
	}
}

func TestPostgreSQLPause_MissingClusterErrors(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")

	if err := prov.Pause(context.Background(), pauseTestNS, "nope"); err == nil {
		t.Fatal("expected error for missing cluster")
	}
}

// readyAfterClient reports a ready primary only after the cluster has been
// polled readyAfter times, so the resume wait loop is exercised for real.
type readyAfterClient struct {
	*k8s.MockClient
	polls      int
	readyAfter int
}

func (c *readyAfterClient) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := c.MockClient.GetCRD(ctx, gvr, namespace, name)
	if err != nil {
		return nil, err
	}
	c.polls++
	copied := obj.DeepCopy()
	if c.polls > c.readyAfter {
		_ = unstructured.SetNestedField(copied.Object, int64(1), "status", "readyInstances")
	}
	return copied, nil
}

func TestPostgreSQLResume_SetsHibernationOffAndWaitsForReadyPrimary(t *testing.T) {
	mock := k8s.NewMockClient()
	cluster := newHibernationTestCluster(3)
	cluster.SetAnnotations(map[string]string{tierInstancesKey: "3", hibernationAnnotation: hibernationOn})
	mock.CRDs[pauseTestClusterKey] = cluster
	client := &readyAfterClient{MockClient: mock, readyAfter: 3}
	prov := NewPostgreSQLProvisioner(client, "")
	prov.resumeTimeout = time.Second
	prov.resumePoll = time.Millisecond

	if err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	stored := storedCluster(t, mock)
	assertAnnotation(t, stored, hibernationAnnotation, hibernationOff)
	assertAnnotation(t, stored, tierInstancesKey, "3")
	assertInstances(t, stored, 3)
	if client.polls <= client.readyAfter {
		t.Errorf("expected resume to poll until ready: polls=%d", client.polls)
	}
	assertUpdatedNotCreated(t, mock)
}

func TestPostgreSQLResume_StaleHibernatedConditionIsNotReady(t *testing.T) {
	// CNPG keeps status.phase "Cluster in healthy state" while hibernated; only
	// readyInstances and the hibernation condition move. A cluster that still
	// reports Hibernated=True must not count as resumed even if readyInstances
	// were left over.
	mock, prov := setupHibernationTest(t)
	cluster := storedCluster(t, mock)
	_ = unstructured.SetNestedField(cluster.Object, "Cluster in healthy state", "status", "phase")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "status", "readyInstances")
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": hibernationCondition, "status": "True", "reason": "Hibernated"},
	}, "status", "conditions")

	err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject)
	if err == nil || !strings.Contains(err.Error(), resumeTimeoutMsg) {
		t.Fatalf("expected resume timeout while still Hibernated, got %v", err)
	}
}

func TestPostgreSQLResume_TimesOutWhenPrimaryNeverReady(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	storedCluster(t, mock).SetAnnotations(map[string]string{hibernationAnnotation: hibernationOn})

	start := time.Now()
	err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject)
	if err == nil || !strings.Contains(err.Error(), resumeTimeoutMsg) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("resume wait exceeded bound: %v", elapsed)
	}
	// The annotation flip already happened; the operator keeps working even
	// though we stopped waiting.
	assertAnnotation(t, storedCluster(t, mock), hibernationAnnotation, hibernationOff)
}

func TestPostgreSQLResume_ContextCancelledStopsWaiting(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	prov.resumeTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := prov.Resume(ctx, pauseTestNS, pauseTestProject)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	assertAnnotation(t, storedCluster(t, mock), hibernationAnnotation, hibernationOff)
}

func TestPostgreSQLResume_MissingClusterErrors(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")

	if err := prov.Resume(context.Background(), pauseTestNS, "nope"); err == nil {
		t.Fatal("expected error for missing cluster")
	}
}

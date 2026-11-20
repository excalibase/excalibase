package provisioner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestRollback_RunsOnADetachedBoundedContext(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	var cleanupErr error
	var bounded bool
	pc.RegisterCleanup("look at the context", func(ctx context.Context) error {
		cleanupErr = ctx.Err()
		_, bounded = ctx.Deadline()
		return nil
	})
	finished, cancel := context.WithCancel(context.Background())
	cancel()

	pc.Rollback(finished)

	if cleanupErr != nil {
		t.Errorf("cleanup ran on a finished context: %v", cleanupErr)
	}
	if !bounded {
		t.Error("cleanup ran with no deadline")
	}
}

func TestRollback_AHungCleanupDoesNotStarveTheOlderOnes(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	pc.cleanupTimeout = 20 * time.Millisecond
	olderRan := false
	pc.RegisterCleanup("older", func(ctx context.Context) error {
		olderRan = ctx.Err() == nil
		return ctx.Err()
	})
	pc.RegisterCleanup("hung", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	var results []CleanupResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		results = pc.Rollback(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hung cleanup was not cut off")
	}

	if len(results) != 2 || results[0].OK || !results[1].OK || !olderRan {
		t.Fatalf("results %+v, older ran=%v: want the hung one failed and the older one done", results, olderRan)
	}
}

// The real client against a fake API server: a cancelled context never reaches
// the API server, which is how the namespace leaked.
func TestRollback_ReachesTheAPIServerWithTheRealClientAfterCancellation(t *testing.T) {
	var deletes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
	}))
	defer api.Close()
	client, err := k8s.NewClientWith(k8s.ClientOptions{APIURL: api.URL, BearerToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pc := NewProvisionContext(nil, nil)
	pc.RegisterCleanup("delete namespace", func(ctx context.Context) error { return client.DeleteNamespace(ctx, "ns") })
	finished, cancel := context.WithCancel(context.Background())
	cancel()

	results := pc.Rollback(finished)

	if deletes.Load() != 1 || !results[0].OK {
		t.Fatalf("%d DELETEs reached the API server, results %+v; want 1, OK", deletes.Load(), results)
	}
}

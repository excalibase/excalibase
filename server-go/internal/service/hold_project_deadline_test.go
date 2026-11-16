package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// stuckInstances answers the project read only when its context ends: a main
// pool with no free connection.
type stuckInstances struct{ *fakestore.Instances }

func (stuckInstances) FindByProjectIDContext(ctx context.Context, _ string) (*domain.DatabaseInstance, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// holdProject reads the project while it holds the lock. That read must give
// up (and give the lock back) rather than wait on the pool forever.
func TestHoldProject_BoundsTheReadMadeUnderTheLock(t *testing.T) {
	svc := NewProvisioningService(stuckInstances{fakestore.NewInstances()}, provisioner.NewFactory(), k8s.NewMockClient())
	svc.projectReadTimeout = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, _, err := svc.holdProject(context.Background(), "p1", OperationPause, func(string, string) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if !storage.IsUnavailable(err) {
			t.Fatalf("err = %v, want one that reads as the database being unavailable", err)
		}
		if !errors.Is(err, storage.ErrPoolBusy) {
			t.Fatalf("err = %v, want ErrPoolBusy so the handlers answer 503 with Retry-After", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read under the lock waited without a deadline")
	}
	// The lock came back: another claim on the project succeeds.
	release, claimed, err := svc.claimer().Claim(context.Background(), "p1", OperationPause)
	if err != nil || !claimed {
		t.Fatalf("lock was not released: claimed=%v err=%v", claimed, errors.Unwrap(err))
	}
	release()
}

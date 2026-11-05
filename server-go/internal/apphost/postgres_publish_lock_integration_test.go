//go:build integration

package apphost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// One project's publishes wait for each other across replicas; another project's do not.
func TestPGPublishLock_SerializesOneProjectOnly(t *testing.T) {
	newPGAppStore(t)
	replicaA, replicaB := apphost.NewPostgresPublishLock(sharedDB), apphost.NewPostgresPublishLock(sharedDB)
	ctx := context.Background()
	unlock, err := replicaA.Lock(ctx, "proj_publish_lock")
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := replicaB.Lock(waiting, "proj_publish_lock"); err == nil {
		t.Fatal("two publishes of one project held the lock at once")
	}
	other, err := replicaB.Lock(ctx, "proj_publish_other")
	if err != nil {
		t.Fatalf("another project waited: %v", err)
	}
	other()

	acquired := make(chan error, 1)
	go func() {
		next, err := replicaB.Lock(ctx, "proj_publish_lock")
		if err == nil {
			next()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("taken while held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(errors.New("the lock was not released"))
	}
}

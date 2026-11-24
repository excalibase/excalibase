package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type recordingEndpoints struct{ withdrawn, published []string }

func (r *recordingEndpoints) Withdraw(_ context.Context, inst *domain.DatabaseInstance) error {
	r.withdrawn = append(r.withdrawn, inst.ProjectID)
	return nil
}
func (r *recordingEndpoints) Publish(_ context.Context, inst *domain.DatabaseInstance) error {
	r.published = append(r.published, inst.ProjectID)
	return nil
}
func (r *recordingEndpoints) Release(context.Context, *domain.DatabaseInstance) error { return nil }

func TestSuspendedEndpoints_ClosesThePortAndPublishesItOnRestore(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["live"] = &domain.DatabaseInstance{ProjectID: "live", Status: string(domain.StatusActive)}
	instances.Items["asleep"] = &domain.DatabaseInstance{ProjectID: "asleep", Status: string(domain.StatusPaused)}
	endpoints := &recordingEndpoints{}
	step := NewSuspendedEndpoints(instances, endpoints)
	ctx := context.Background()

	for _, id := range []string{"live", "asleep", "unknown"} {
		if err := step.Stop(ctx, id); err != nil {
			t.Fatalf("Stop %s: %v", id, err)
		}
		if err := step.Restore(ctx, id); err != nil {
			t.Fatalf("Restore %s: %v", id, err)
		}
	}
	if len(endpoints.withdrawn) != 2 {
		t.Fatalf("withdrawn = %v, want both known projects closed", endpoints.withdrawn)
	}
	if len(endpoints.published) != 1 || endpoints.published[0] != "live" {
		t.Fatalf("published = %v, want only the running project: a paused one comes back with its resume", endpoints.published)
	}
}

type recordingWorkloads struct{ stopped, restarted []string }

func (r *recordingWorkloads) StopProjectWorkloads(_ context.Context, id string) error {
	r.stopped = append(r.stopped, id)
	return errors.New("pods did not drain")
}
func (r *recordingWorkloads) RestartFunctionRuntime(_ context.Context, id string) error {
	r.restarted = append(r.restarted, id)
	return nil
}

func TestSuspendedWorkloads_StopAndRestartTheFunctionRuntime(t *testing.T) {
	workloads := &recordingWorkloads{}
	step := NewSuspendedWorkloads(workloads)
	if err := step.Stop(context.Background(), "p1"); err == nil {
		t.Fatal("a stop that failed must say so")
	}
	if err := step.Restore(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if len(workloads.stopped) != 1 || len(workloads.restarted) != 1 {
		t.Fatalf("stopped %v restarted %v", workloads.stopped, workloads.restarted)
	}
}

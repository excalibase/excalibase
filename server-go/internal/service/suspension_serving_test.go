package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/orgguard"

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
	step := NewSuspendedEndpoints(instances, endpoints, nil)
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
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: string(domain.StatusActive)}
	step := NewSuspendedWorkloads(workloads, instances, nil)
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

type recordingRouter struct{ routed, unrouted []string }

func (r *recordingRouter) UnrouteProjectSites(_ context.Context, id string) error {
	r.unrouted = append(r.unrouted, id)
	return nil
}
func (r *recordingRouter) RouteProjectSites(_ context.Context, id string) error {
	r.routed = append(r.routed, id)
	return nil
}

// An unsuspend brings back only what may serve now: not a project awaiting
// deletion or in teardown or restore, and not one whose org owner is still
// suspended. The admin's override must not lift either.
func TestUnsuspendRestoresOnlyWhatMayServe(t *testing.T) {
	instances := fakestore.NewInstances()
	for id, inst := range map[string]*domain.DatabaseInstance{
		"fine":     {ProjectID: "fine", OrgID: "o-fine", Status: string(domain.StatusActive)},
		"pending":  {ProjectID: "pending", OrgID: "o-fine", Status: string(domain.StatusPendingDeletion)},
		"deleting": {ProjectID: "deleting", OrgID: "o-fine", Status: string(domain.StatusDeleting)},
		"restore":  {ProjectID: "restore", OrgID: "o-fine", Status: string(domain.StatusRestoring)},
		"held":     {ProjectID: "held", OrgID: "o-still", Status: string(domain.StatusActive)},
	} {
		instances.Items[id] = inst
	}
	guard := orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		return map[string]bool{"o-still": true}, nil
	})
	admin := orgguard.WithPlatformOverride(context.Background())

	router, workloads, endpoints := &recordingRouter{}, &recordingWorkloads{}, &recordingEndpoints{}
	steps := []interface {
		Restore(context.Context, string) error
	}{NewSuspendedSites(router, instances, guard), NewSuspendedWorkloads(workloads, instances, guard), NewSuspendedEndpoints(instances, endpoints, guard)}
	for _, id := range []string{"fine", "pending", "deleting", "restore", "held", "unknown"} {
		for _, step := range steps {
			if err := step.Restore(admin, id); err != nil {
				t.Fatalf("Restore %s: %v", id, err)
			}
		}
	}
	for name, got := range map[string][]string{"sites": router.routed, "function runtime": workloads.restarted, "ports": endpoints.published} {
		if len(got) != 1 || got[0] != "fine" {
			t.Errorf("%s restored %v, want only the project that may serve", name, got)
		}
	}
}

func TestUnsuspendRestoreFailsWhenTheStandingCannotBeRead(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["p"] = &domain.DatabaseInstance{ProjectID: "p", OrgID: "o", Status: string(domain.StatusActive)}
	guard := orgguard.New(instances, func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") })
	router := &recordingRouter{}
	if err := NewSuspendedSites(router, instances, guard).Restore(context.Background(), "p"); err == nil || len(router.routed) != 0 {
		t.Fatalf("err = %v, routed %v: an unreadable standing must not restore", err, router.routed)
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// endpointRecorder records what the lifecycle asks of the public endpoint,
// in order, so a test can assert that a paused project stops answering and a
// resumed one starts again.
type endpointRecorder struct {
	log          *[]string
	withdrawErr  error
	publishErr   error
	releaseErr   error
	releaseCount int
}

func (r *endpointRecorder) Withdraw(context.Context, *domain.DatabaseInstance) error {
	*r.log = append(*r.log, "withdraw-endpoint")
	return r.withdrawErr
}

func (r *endpointRecorder) Publish(context.Context, *domain.DatabaseInstance) error {
	*r.log = append(*r.log, "publish-endpoint")
	return r.publishErr
}

func (r *endpointRecorder) Release(context.Context, *domain.DatabaseInstance) error {
	*r.log = append(*r.log, "release-endpoint")
	r.releaseCount++
	return r.releaseErr
}

var _ PublicEndpointReconciler = (*endpointRecorder)(nil)

func TestPauseWithdrawsThePublicEndpointOnceTheWorkloadIsStopped(t *testing.T) {
	f := newObservedPause(t)
	rec := &endpointRecorder{log: &f.pauser.log}
	f.svc.endpoints = rec

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := lastTwo(f.pauser.log); got != "pause>withdraw-endpoint" {
		t.Fatalf("sequence ended %q; the endpoint must go once the database is stopped", got)
	}
}

func TestPauseFailsWhenThePublicEndpointIsNotWithdrawn(t *testing.T) {
	f := newObservedPause(t)
	f.svc.endpoints = &endpointRecorder{log: &f.pauser.log, withdrawErr: errors.New("apiserver said no")}

	if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("Pause error = %v, want ErrPauseNotObserved", err)
	}
	inst, err := f.store.FindByProjectID(observedPauseProject)
	if err != nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if inst.Status != string(domain.StatusPausing) {
		t.Fatalf("status = %q, want PAUSING so a retry converges", inst.Status)
	}
}

func TestResumePublishesThePublicEndpointAfterTheWorkloadIsBack(t *testing.T) {
	f := newObservedPause(t)
	rec := &endpointRecorder{log: &f.pauser.log}
	f.svc.endpoints = rec

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumeAt, publishAt := -1, -1
	for i, call := range f.pauser.log {
		switch call {
		case "resume":
			resumeAt = i
		case "publish-endpoint":
			publishAt = i
		}
	}
	if resumeAt < 0 || publishAt < 0 || publishAt < resumeAt {
		t.Fatalf("log = %v; the endpoint must come back after the workload", f.pauser.log)
	}
}

func TestResumeFailsWhenThePublicEndpointIsNotPublished(t *testing.T) {
	f := newObservedPause(t)
	f.svc.endpoints = &endpointRecorder{log: &f.pauser.log}

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.svc.endpoints = &endpointRecorder{log: &f.pauser.log, publishErr: errors.New("apiserver said no")}
	if err := f.svc.Resume(context.Background(), observedPauseProject); !errors.Is(err, ErrResumeNotObserved) {
		t.Fatalf("Resume error = %v, want ErrResumeNotObserved", err)
	}
}

func TestPauseWithNoEndpointReconcilerIsUnaffected(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	for _, call := range f.pauser.log {
		if call == "withdraw-endpoint" {
			t.Fatal("a deployment with no endpoints configured must not be asked about them")
		}
	}
}

func TestDeletionReleasesThePublicEndpointBeforeAnythingElse(t *testing.T) {
	svc := &ProvisioningService{endpoints: &endpointRecorder{log: new([]string)}}
	steps := svc.deletionSteps(false)
	if len(steps) == 0 || steps[0].name != domain.DeletionStepReleaseEndpoint {
		t.Fatalf("first step = %q, want the public endpoint to stop answering first", stepNames(steps))
	}
}

func TestDeletionOmitsTheEndpointStepWhenNoneIsWired(t *testing.T) {
	svc := &ProvisioningService{}
	for _, step := range svc.deletionSteps(false) {
		if step.name == domain.DeletionStepReleaseEndpoint {
			t.Fatal("the step must be absent rather than present and failing")
		}
	}
}

func TestReleasePublicEndpointStepIsIdempotent(t *testing.T) {
	rec := &endpointRecorder{log: new([]string)}
	svc := &ProvisioningService{endpoints: rec}
	inst := &domain.DatabaseInstance{ProjectID: "proj-teardown", Namespace: "org-proj-teardown"}

	for i := 0; i < 2; i++ {
		if err := svc.releasePublicEndpoint(context.Background(), inst); err != nil {
			t.Fatalf("releasePublicEndpoint: %v", err)
		}
	}
	if rec.releaseCount != 2 {
		t.Fatalf("release calls = %d, want the retried teardown to run it again", rec.releaseCount)
	}
}

func lastTwo(log []string) string {
	if len(log) < 2 {
		return ""
	}
	return log[len(log)-2] + ">" + log[len(log)-1]
}

func stepNames(steps []deletionStep) string {
	names := ""
	for _, step := range steps {
		names += step.name + " "
	}
	return names
}

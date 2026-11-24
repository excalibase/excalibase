package adminconsole

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingStep struct {
	name      string
	log       *[]string
	failStop  map[string]error
	failRestr map[string]error
}

func (r recordingStep) Name() string { return r.name }

func (r recordingStep) Stop(_ context.Context, projectID string) error {
	*r.log = append(*r.log, "stop "+r.name+" "+projectID)
	return r.failStop[projectID]
}

func (r recordingStep) Restore(_ context.Context, projectID string) error {
	*r.log = append(*r.log, "restore "+r.name+" "+projectID)
	return r.failRestr[projectID]
}

func TestSuspendStopsServingOfEveryProjectAfterThePause(t *testing.T) {
	svc, _, control, _ := newSuspensionFixture()
	var log []string
	svc.AddServingSteps(recordingStep{name: "sites", log: &log}, recordingStep{name: "ports", log: &log})

	result, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "proxy abuse"})
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	want := []string{"stop sites p1", "stop ports p1", "stop sites p2", "stop ports p2"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("steps = %v, want %v", log, want)
	}
	if len(control.paused) == 0 || len(result.ServingFailed) != 0 {
		t.Fatalf("paused %v, failed %v", control.paused, result.ServingFailed)
	}
}

func TestSuspendKeepsTheSuspensionWhenAStepFails(t *testing.T) {
	svc, store, _, _ := newSuspensionFixture()
	var log []string
	svc.AddServingSteps(
		recordingStep{name: "sites", log: &log, failStop: map[string]error{"p1": errors.New("cloudflare down")}},
		recordingStep{name: "ports", log: &log},
	)

	result, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "proxy abuse"})
	if err != nil {
		t.Fatalf("a failed step must not fail the suspension: %v", err)
	}
	if store.users["victim"].Active {
		t.Fatal("account must stay suspended")
	}
	if len(result.ServingFailed) != 1 || result.ServingFailed[0].Step != "sites" || result.ServingFailed[0].ProjectID != "p1" {
		t.Fatalf("ServingFailed = %+v", result.ServingFailed)
	}
	if len(log) != 4 {
		t.Fatalf("the other steps and projects must still run: %v", log)
	}
}

func TestUnsuspendRestoresServing(t *testing.T) {
	svc, _, _, _ := newSuspensionFixture()
	var log []string
	svc.AddServingSteps(recordingStep{name: "sites", log: &log})
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "proxy abuse"}); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	log = nil

	if _, err := svc.Unsuspend(context.Background(), admin, "victim", "appealed"); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	want := []string{"restore sites p1", "restore sites p2"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("restore steps = %v, want %v", log, want)
	}
}

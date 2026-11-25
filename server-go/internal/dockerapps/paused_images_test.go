package dockerapps

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestPausedAppImagesReadsWhatTheStoppedContainersWouldRun(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	if _, err := h.rt.PausedAppImages(ctx, testDB, "app-01", "web"); !errors.Is(err, k8s.ErrAppNotPaused) {
		t.Fatalf("running app: err = %v, want ErrAppNotPaused", err)
	}
	if err := h.rt.PauseAppWorkload(ctx, testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	// The container is what resumes, so its image is what is read, whatever any record says.
	h.engine.appContainers("app-01")[0].config.Image = "ttl.sh/test:1h"

	images, err := h.rt.PausedAppImages(ctx, testDB, "app-01", "web")

	if err != nil || len(images) != 1 || images[0] != "ttl.sh/test:1h" {
		t.Fatalf("images = %v, %v; want the stopped container's own image", images, err)
	}
}

func TestPausedAppImagesOfAnAppNeverDeployed(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.rt.PausedAppImages(context.Background(), testDB, "nope", "web"); !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("err = %v, want ErrAppNotDeployed", err)
	}
}

// faultyInspect answers ContainerInspect with what the test sets, and everything else as the engine would.
type faultyInspect struct {
	Engine
	err   error
	strip bool
}

func (f *faultyInspect) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if f.err != nil {
		return container.InspectResponse{}, f.err
	}
	response, err := f.Engine.ContainerInspect(ctx, id)
	if f.strip {
		response.Config = nil
	}
	return response, err
}

func pausedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	if err := h.rt.PauseAppWorkload(context.Background(), testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPausedAppImagesFailsClosedWhenTheEngineCannotInspectAContainer(t *testing.T) {
	h := pausedHarness(t)
	h.rt.engine = &faultyInspect{Engine: h.rt.engine, err: errors.New("engine unreachable")}

	images, err := h.rt.PausedAppImages(context.Background(), testDB, "app-01", "web")

	if err == nil || len(images) != 0 {
		t.Fatalf("images = %v, err = %v; want an error and no images", images, err)
	}
}

func TestPausedAppImagesFailsClosedWhenTheEngineAnswersNoConfiguration(t *testing.T) {
	h := pausedHarness(t)
	h.rt.engine = &faultyInspect{Engine: h.rt.engine, strip: true}

	images, err := h.rt.PausedAppImages(context.Background(), testDB, "app-01", "web")

	if !errors.Is(err, k8s.ErrAppImageUnreadable) || len(images) != 0 {
		t.Fatalf("images = %v, err = %v; want ErrAppImageUnreadable", images, err)
	}
}

package dockerapps

import (
	"context"
	"errors"
	"testing"

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

package dockerapps

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// PausedAppImages reads the image of each stopped container a resume would
// start, as the engine holds it, not as the app's record says.
func (r *Runtime) PausedAppImages(ctx context.Context, _, appID, appName string) ([]string, error) {
	_, newest, err := r.newestDeploy(ctx, appID, appName)
	if err != nil {
		return nil, err
	}
	images := make([]string, 0, len(newest))
	for _, summary := range newest {
		inspect, err := r.engine.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", summary.ID, err)
		}
		if inspect.Config == nil {
			return nil, fmt.Errorf("read %s: the engine answered no configuration: %w", summary.ID, k8s.ErrAppImageUnreadable)
		}
		images = append(images, inspect.Config.Image)
	}
	return images, nil
}

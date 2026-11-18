package dockerapps

import "context"

func (r *Runtime) PausedAppImages(ctx context.Context, _ string, appID, appName string) ([]string, error) {
	return nil, nil
}

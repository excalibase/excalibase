package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// errPausedImageUnreadable refuses a resume whose paused workload names no usable image.
var errPausedImageUnreadable = k8s.ErrAppImageUnreadable

// SetProxyGuard refuses a deploy of a proxy or VPN node (EXC-755); nil allows all.
func (s *AppDeployService) SetProxyGuard(guard *abuse.AppGuard) { s.proxyGuard = guard }

// refuseProxy is asked by every deploy path through record. A deploy of zero
// replicas runs nothing, so it stays possible: it is how a node is stopped.
func (s *AppDeployService) refuseProxy(ctx context.Context, app *apphost.App, cfg apphost.DeployConfig, actor string) error {
	if cfg.Replicas == 0 {
		return nil
	}
	return s.proxyGuard.Check(ctx, appSubject(app, cfg.Image, cfg.Env, actor, "deploy"))
}

func appSubject(app *apphost.App, image string, env []apphost.EnvVar, actor, surface string) abuse.AppSubject {
	names := make([]string, 0, len(env))
	for _, variable := range env {
		names = append(names, variable.Name)
	}
	return abuse.AppSubject{
		ProjectID: app.ProjectID, AppID: app.ID, Name: app.Name, Image: image,
		EnvNames: names, Actor: actor, Surface: surface,
	}
}

// refuseProxyOnResume checks what a resume would bring back: the app as it is
// now, the config of the deploy that last ran, and the images the paused
// workload itself holds, which are what actually run. The last is read from the
// runtime and a resume that cannot read it is refused, never allowed. The app
// is left stopped when any of them is refused.
func (s *AppDeployService) refuseProxyOnResume(ctx context.Context, namespace string, app *apphost.App, actor string) error {
	if s.proxyGuard == nil {
		return nil
	}
	if err := s.proxyGuard.Check(ctx, appSubject(app, app.Image, app.Env, actor, "resume")); err != nil {
		return err
	}
	last, err := s.deploys.GetLastSucceeded(app.ProjectID, app.ID)
	if err != nil {
		return fmt.Errorf("look up the last deploy: %w", err)
	}
	if last != nil {
		if err := s.proxyGuard.Check(ctx, appSubject(app, last.Config.Image, last.Config.Env, actor, "resume")); err != nil {
			return err
		}
	}
	return s.refusePausedImages(ctx, namespace, app, actor)
}

func (s *AppDeployService) refusePausedImages(ctx context.Context, namespace string, app *apphost.App, actor string) error {
	images, err := s.runtime.PausedAppImages(ctx, namespace, app.ID, app.Name)
	if err != nil {
		return fmt.Errorf("read the images the paused workload would run: %w", err)
	}
	if len(images) == 0 {
		return errPausedImageUnreadable
	}
	for _, image := range images {
		if strings.TrimSpace(image) == "" {
			return errPausedImageUnreadable
		}
		if err := s.proxyGuard.Check(ctx, appSubject(app, image, app.Env, actor, "resume")); err != nil {
			return err
		}
	}
	return nil
}

package service

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
)

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

package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// registryTokenPath is the token service zot forwards a node's login to (its upstream token endpoint).
const registryTokenPath = "/internal/registry/token"

// newInternalRegistry is the platform's registry for git builds (EXC-540),
// nil while app git builds are off or no registry is configured.
func newInternalRegistry(cfg config.AppConfig) (*appregistry.Internal, error) {
	if cfg.AppRegistry.Host == "" || !features.Enabled(context.Background(), cfg.Features(), features.AppGitBuilds) {
		return nil, nil
	}
	issuer, err := appregistry.NewIssuer(cfg.AppRegistry.TokenKeyPEM, cfg.AppRegistry.Host)
	if err != nil {
		return nil, err
	}
	return appregistry.NewInternal(cfg.AppRegistry.Host, issuer)
}

// projectExists is the token service's check that a pull credential's project is still there.
func projectExists(instances storage.InstanceStore) appregistry.ProjectExists {
	return func(_ context.Context, projectID string) (bool, error) {
		_, err := instances.FindByProjectID(projectID)
		if errors.Is(err, storage.ErrProjectNotFound) {
			return false, nil
		}
		return err == nil, err
	}
}

func newRegistryTokenHandler(internal *appregistry.Internal, instances storage.InstanceStore) http.Handler {
	if internal == nil {
		return nil
	}
	return appregistry.NewTokenHandler(internal.Issuer(), projectExists(instances))
}

// mountRegistryToken serves the token service on the internal listener only, and only while app git builds are on.
func mountRegistryToken(r *chi.Mux, d *handlerDeps) {
	if d.registryToken == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(features.Require(d.features, features.AppGitBuilds))
		r.Use(custommw.RequireInternalListener)
		r.Handle(registryTokenPath, d.registryToken)
	})
}

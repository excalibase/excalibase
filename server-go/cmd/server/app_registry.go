package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// registryTokenPath is the token service zot forwards a node's login to (its upstream token endpoint).
const registryTokenPath = "/internal/registry/token"

// newInternalRegistry is the platform's registry for git builds (EXC-540):
// with a host configured its name is refused in every image a caller names,
// flag or not; it issues tokens and pull credentials only while app git
// builds are on and the vault holds the projects' pull generations.
func newInternalRegistry(cfg config.AppConfig, secrets appregistry.SecretStore) (*appregistry.Internal, error) {
	if cfg.AppRegistry.Host == "" {
		return nil, nil
	}
	guard, err := appregistry.NewInternal(cfg.AppRegistry.Host, cfg.AppRegistry.Aliases)
	if err != nil {
		return nil, err
	}
	if !features.Enabled(context.Background(), cfg.Features(), features.AppGitBuilds) {
		return guard, nil
	}
	if secrets == nil {
		return nil, errors.New("app git builds need the vault for the registry's pull generations")
	}
	issuer, err := appregistry.NewIssuer(cfg.AppRegistry.TokenKeyPEM, cfg.AppRegistry.Host)
	if err != nil {
		return nil, err
	}
	return guard.Issuing(issuer, appregistry.NewGenerations(secrets)), nil
}

// projectExists is the token service's check that a pull credential's
// project is still there: the store answers a missing one with nil, and a
// project being deleted pulls nothing either.
func projectExists(instances storage.InstanceStore) appregistry.ProjectExists {
	return func(_ context.Context, projectID string) (bool, error) {
		inst, err := instances.FindByProjectID(projectID)
		if err != nil {
			return false, err
		}
		if inst == nil || domain.IsDeletionStatus(inst.Status) || inst.Status == string(domain.StatusPendingDeletion) {
			return false, nil
		}
		return true, nil
	}
}

// projectKept is the sweep's check: only a project gone for good loses its
// images; one whose deletion can still be cancelled keeps them.
func projectKept(instances storage.InstanceStore) appregistry.ProjectExists {
	return func(_ context.Context, projectID string) (bool, error) {
		inst, err := instances.FindByProjectID(projectID)
		if err != nil {
			return false, err
		}
		return inst != nil && !domain.IsDeletionStatus(inst.Status), nil
	}
}

func newRegistryTokenHandler(internal *appregistry.Internal, instances storage.InstanceStore) http.Handler {
	// Refused while the registry only guards: then there is no token service.
	h, err := appregistry.NewTokenHandler(internal, projectExists(instances))
	if err != nil {
		return nil
	}
	return h
}

// mountRegistryToken serves the token service on the internal listener only, and only while app git builds are on.
func mountRegistryToken(r *chi.Mux, d *handlerDeps) {
	if d.registryToken == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(features.Require(d.features, features.AppGitBuilds))
		r.Use(custommw.RequireInternalListener)
		// A node asks once per image pull; edge-function code shares this listener.
		r.Use(custommw.RateLimit(custommw.PerIP, registryTokenCallsPerMinute, time.Minute))
		r.Method(http.MethodGet, registryTokenPath, d.registryToken)
		r.Method(http.MethodPost, registryTokenPath, d.registryToken)
	})
}

// vaultSecrets keeps a missing vault a nil interface, not a typed nil.
func vaultSecrets(vc vaultclient.VaultClient) appregistry.SecretStore {
	if vc == nil {
		return nil
	}
	return vc
}

// newInternalRegistryHandler serves the rotation only where pull credentials are issued.
func newInternalRegistryHandler(internal *appregistry.Internal, secrets appregistry.SecretStore) *handler.InternalRegistryHandler {
	if !internal.CanIssue() || secrets == nil {
		return nil
	}
	return handler.NewInternalRegistryHandler(appregistry.NewGenerations(secrets))
}

// mountInternalRegistry lets a project's Admin retire its pull credential.
func mountInternalRegistry(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	if d.internalRegistryH == nil {
		return
	}
	r.Route("/api/projects/{projectId}/internal-registry", func(r chi.Router) {
		r.Use(features.Require(d.features, features.AppGitBuilds))
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleAdmin, store, sqlStore))
		r.Use(d.rlAuthed)
		r.Use(d.activity)
		d.internalRegistryH.Routes(r)
	})
}

const (
	registryTokenCallsPerMinute       = 600
	appRegistrySweepLockID      int64 = 0x6168_0acb_5e9c_0540
	appRegistrySweepInterval          = 24 * time.Hour
)

// startRegistrySweeper deletes built images nothing will run again, daily on
// the leading replica; only an issuing registry holds images of ours.
func startRegistrySweeper(cfg config.AppConfig, sqlStore storage.PlatformStore, instances storage.InstanceStore, internal *appregistry.Internal) (func(), error) {
	noop := func() {
		// app git builds off or no platform store: no images of ours to sweep
	}
	if !internal.CanIssue() || sqlStore == nil {
		return noop, nil
	}
	client, err := internal.Client(cfg.AppRegistry.URL)
	if err != nil {
		return noop, err
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), appRegistrySweepLockID)
	}
	sweeper := service.NewRegistrySweeper(client, apphost.NewPostgresAppStore(sqlStore.DB()), apphost.NewPostgresDeployStore(sqlStore.DB()), projectKept(instances))
	return sweeper.Start(context.Background(), service.NewLeadership(lock), appRegistrySweepInterval), nil
}

package service

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// appImagePublisher is AppBuildPublisher as the queue adapter uses it.
type appImagePublisher interface {
	Publish(ctx context.Context, result AppBuildResult) (*apphost.Deploy, error)
}

// AppGetter finds a project's app; nil for one it does not hold.
type AppGetter interface {
	Get(projectID, id string) (*apphost.App, error)
}

// appBuildQueuePublisher hands the build queue's finished app builds to the publisher. It lives here,
// not in sitebuilds, because sitebuilds may not import service: the refusals are named for it as kinds.
type appBuildQueuePublisher struct {
	publisher appImagePublisher
	apps      AppGetter
}

// NewAppBuildQueuePublisher is the build dispatcher's AppPublisher over an AppBuildPublisher.
func NewAppBuildQueuePublisher(publisher *AppBuildPublisher, apps AppGetter) sitebuilds.AppPublisher {
	return newAppBuildQueuePublisher(publisher, apps)
}

func newAppBuildQueuePublisher(publisher appImagePublisher, apps AppGetter) *appBuildQueuePublisher {
	return &appBuildQueuePublisher{publisher: publisher, apps: apps}
}

var errAppGoneMidBuild = errors.New("the app was deleted while it built; nothing was deployed")

func (p *appBuildQueuePublisher) PublishBuild(ctx context.Context, publish sitebuilds.AppPublish) (string, error) {
	app, err := p.apps.Get(publish.ProjectID, publish.AppID)
	if err != nil {
		return "", err
	}
	if app == nil {
		return "", &sitebuilds.BuildFailure{Kind: sitebuilds.FailDeploy, Err: errAppGoneMidBuild}
	}
	deploy, err := p.publisher.Publish(ctx, AppBuildResult{BuildID: publish.BuildID, ProjectID: publish.ProjectID,
		CommitSHA: publish.CommitSHA, App: app, MaxImage: publish.MaxImageBytes})
	if err != nil {
		return "", namedFailure(err)
	}
	return deploy.ID, nil
}

// namedFailure gives each refusal the user can act on its own failure kind; anything else stays unnamed.
func namedFailure(err error) error {
	kinds := []struct {
		cause error
		kind  sitebuilds.FailureKind
	}{
		{ErrImageTooLarge, appbuilds.FailImageTooLarge},
		{ErrRegistryFull, appbuilds.FailRegistryFull},
		{appregistry.ErrBadArchive, appbuilds.FailBadImage},
	}
	for _, named := range kinds {
		if errors.Is(err, named.cause) {
			return &sitebuilds.BuildFailure{Kind: named.kind, Err: err}
		}
	}
	return err
}

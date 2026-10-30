package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// EnableApps wires git builds of apps; the appgitbuilds flag still decides, per call, whether they are used. Call it before serving.
func (s *Service) EnableApps(support AppSupport) {
	s.apps = &support
}

// appsOn answers whether app builds are wired here and switched on now.
func (s *Service) appsOn(ctx context.Context) bool {
	return s != nil && s.apps != nil && features.Enabled(ctx, s.apps.Flags, features.AppGitBuilds)
}

func (s *Service) mustApp(ctx context.Context, projectID, appID string) error {
	if err := s.Available(); err != nil {
		return err
	}
	if !s.appsOn(ctx) {
		return ErrAppBuildsOff
	}
	app, err := s.apps.Apps.Get(projectID, appID)
	if err == nil && app == nil {
		return apphost.ErrAppNotFound
	}
	return err
}

func appTarget(projectID, appID string) Target {
	return Target{Kind: KindApp, ProjectID: projectID, ID: appID}
}

// AppConnectRequest connects a repository, named by id or by owner/name, to an app.
type AppConnectRequest struct {
	ProjectID    string
	AppID        string
	UserID       string
	RepositoryID int64
	Repository   string
	Settings     AppSettings
}

// AppConnectResult is the saved connection and the first build it queued.
type AppConnectResult struct {
	Connection *AppConnection `json:"connection"`
	Build      *Build         `json:"build,omitempty"`
	// BuildError says why no first build was queued; the connection is saved regardless.
	BuildError string `json:"buildError,omitempty"`
}

// ConnectApp checks the user's own sign-in shows them the repository and the App still reaches it,
// saves the connection, and queues a first build.
func (s *Service) ConnectApp(ctx context.Context, request AppConnectRequest) (*AppConnectResult, error) {
	if err := s.mustApp(ctx, request.ProjectID, request.AppID); err != nil {
		return nil, err
	}
	installation, repository, err := s.resolveRepository(ctx, request.UserID, request.RepositoryID, request.Repository)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Settings.Branch) == "" {
		request.Settings.Branch = repository.DefaultBranch
	}
	settings, err := request.Settings.Normalise()
	if err != nil {
		return nil, err
	}
	connection := &AppConnection{AppID: request.AppID, GitSource: s.newSource(request.ProjectID, request.UserID, installation, repository),
		Settings: settings}
	if err := s.store.SaveAppConnection(ctx, connection); err != nil {
		return nil, err
	}
	result := &AppConnectResult{Connection: connection}
	build, err := s.buildHead(ctx, connection, request.UserID)
	if err != nil {
		result.BuildError = userMessage(err)
	}
	result.Build = build
	return result, nil
}

// AppConnection answers the app's connection, or nil when none.
func (s *Service) AppConnection(ctx context.Context, projectID, appID string) (*AppConnection, error) {
	if err := s.mustApp(ctx, projectID, appID); err != nil {
		return nil, err
	}
	connection, err := s.store.GetAppConnection(ctx, projectID, appID)
	if err != nil || connection == nil {
		return connection, err
	}
	if err := s.checkConnector(ctx, connection); err != nil && !errors.Is(err, ErrConnectionInactive) {
		return nil, err
	}
	return connection, nil
}

// DisconnectApp forgets the repository and cancels the app's queued and running builds.
// The flag never blocks it, and the store scopes it by project.
func (s *Service) DisconnectApp(ctx context.Context, projectID, appID string) error {
	if err := s.Available(); err != nil {
		return err
	}
	if err := s.store.DeleteAppConnection(ctx, projectID, appID); err != nil {
		return err
	}
	return s.store.CancelBuilds(ctx, appTarget(projectID, appID), disconnectedReason)
}

// RebuildApp queues a build of the connected branch's head.
func (s *Service) RebuildApp(ctx context.Context, projectID, appID, actor string) (*Build, error) {
	if err := s.mustApp(ctx, projectID, appID); err != nil {
		return nil, err
	}
	connection, err := s.store.GetAppConnection(ctx, projectID, appID)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, ErrNotConnected
	}
	return s.buildHead(ctx, connection, actor)
}

func (s *Service) ListAppBuilds(ctx context.Context, projectID, appID string) ([]*Build, error) {
	if err := s.mustApp(ctx, projectID, appID); err != nil {
		return nil, err
	}
	return s.store.ListBuilds(ctx, appTarget(projectID, appID), buildsListLimit)
}

func (s *Service) GetAppBuild(ctx context.Context, projectID, appID, id string) (*Build, error) {
	if err := s.mustApp(ctx, projectID, appID); err != nil {
		return nil, err
	}
	return s.oneBuild(ctx, appTarget(projectID, appID), id)
}

// sizedAppBuild snapshots the app's settings and the plan's app build limits, image cap included.
func (s *Service) sizedAppBuild(ctx context.Context, connection *AppConnection) (*Build, error) {
	if !s.appsOn(ctx) {
		return nil, ErrAppBuildsOff
	}
	limits, err := s.apps.Limits.AppBuildLimits(ctx, connection.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read the plan's app build limits: %w", err)
	}
	return &Build{Kind: KindApp, AppID: connection.AppID, Settings: Settings{Branch: connection.Settings.Branch},
		Limits: limits.Limits, App: &AppBuild{AppSettings: connection.Settings, MaxImageBytes: limits.MaxImageBytes},
		buildsPerHour: limits.MaxBuildsPerHour}, nil
}

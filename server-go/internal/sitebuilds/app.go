package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// App builders (EXC-547): auto picks dockerfile when the commit has one, buildpacks otherwise.
const (
	AppBuilderAuto       = "auto"
	AppBuilderDockerfile = "dockerfile"
	AppBuilderBuildpacks = "buildpacks"
)

// AppBuilders are the builders an app connection may name, for Studio and MCP to offer.
var AppBuilders = []string{AppBuilderAuto, AppBuilderDockerfile, AppBuilderBuildpacks}

// ErrAppBuildsOff is an install where git builds of apps are not switched on (flag appgitbuilds) or not wired.
var ErrAppBuildsOff = errors.New("git builds of apps are not switched on for this install")

// AppSettings are how an app's repository becomes an image.
type AppSettings struct {
	Branch  string `json:"branch"`
	Builder string `json:"builder"`
	// RootDir is the build context inside the repository; DockerfilePath is relative to it.
	RootDir        string `json:"rootDir"`
	DockerfilePath string `json:"dockerfilePath"`
	// BuildArgs are plain values; they end up in the image's history, so secrets never go here.
	BuildArgs map[string]string `json:"buildArgs"`
}

// Normalise trims and checks the settings, answering an InputError for anything refused.
func (s AppSettings) Normalise() (AppSettings, error) {
	out := s
	out.Branch = strings.TrimSpace(s.Branch)
	out.Builder = strings.TrimSpace(s.Builder)
	if out.Builder == "" {
		out.Builder = AppBuilderAuto
	}
	var err error
	if out.RootDir, err = cleanRelative("root directory", s.RootDir, true); err != nil {
		return AppSettings{}, err
	}
	if out.DockerfilePath, err = cleanRelative("Dockerfile path", s.DockerfilePath, true); err != nil {
		return AppSettings{}, err
	}
	if err := errors.Join(validBranch(out.Branch), validBuilder(out.Builder), validBuildArgs(s.BuildArgs)); err != nil {
		return AppSettings{}, &InputError{Reason: err.Error()}
	}
	out.BuildArgs = make(map[string]string, len(s.BuildArgs))
	for name, value := range s.BuildArgs {
		out.BuildArgs[name] = value
	}
	return out, nil
}

func validBuilder(builder string) error {
	switch builder {
	case AppBuilderAuto, AppBuilderDockerfile, AppBuilderBuildpacks:
		return nil
	}
	return fmt.Errorf("the builder %q is not offered; choose auto, dockerfile or buildpacks", builder)
}

func validBuildArgs(args map[string]string) error {
	for name := range args {
		if strings.HasPrefix(strings.ToUpper(name), "BUILDKIT_") {
			return fmt.Errorf("build argument %s is reserved for the builder", name)
		}
	}
	return validVariables("build argument", args, nil)
}

// AppConnection is an app's repository and how to build it.
type AppConnection struct {
	AppID string `json:"appId"`
	GitSource
	Settings AppSettings `json:"settings"`
}

func (c *AppConnection) target() Target {
	return Target{Kind: KindApp, ProjectID: c.ProjectID, ID: c.AppID}
}

func (c *AppConnection) branch() string { return c.Settings.Branch }

// AppBuild is an app build's settings and the plan's image cap, fixed when it is queued.
type AppBuild struct {
	AppSettings
	MaxImageBytes int64 `json:"maxImageBytes"`
	// MaxBuildsPerHour is the plan's app builds per hour, enforced as the build queues and not kept.
	MaxBuildsPerHour int `json:"-"`
}

// AppLimits are one app build's plan limits (tier_configs.app_build_*).
type AppLimits struct {
	Limits
	MaxImageBytes int64
	// MaxBuildsPerHour is how many app builds the project may queue in any hour.
	MaxBuildsPerHour int
}

// ErrAppBuildRateLimited is a project past its plan's app builds per hour.
var ErrAppBuildRateLimited = errors.New("this project has queued as many app builds this hour as its plan allows")

// AppBuildRateLimitError says when the project may build an app again.
type AppBuildRateLimitError struct{ RetryAfter time.Duration }

func (e *AppBuildRateLimitError) Error() string {
	return ErrAppBuildRateLimited.Error() + "; try again in " + e.RetryAfter.Round(time.Second).String()
}

func (e *AppBuildRateLimitError) Is(target error) bool { return target == ErrAppBuildRateLimited }

// AppLimitsSource answers a project's app build limits from its plan, and the largest app build any plan runs.
type AppLimitsSource interface {
	AppBuildLimits(ctx context.Context, projectID string) (AppLimits, error)
	LargestAppBuild(ctx context.Context) (BuildSize, error)
}

// AppLookup finds a project's app; nil, or apphost.ErrAppNotFound, for one the project does not hold.
type AppLookup interface {
	Get(projectID, id string) (*apphost.App, error)
}

// AppSupport is what git builds of apps need beside what sites use.
type AppSupport struct {
	Apps   AppLookup
	Limits AppLimitsSource
	// Files resolves builder auto against the commit.
	Files gitprovider.FileChecker
	Flags features.Flags
}

// JobControl is what the dispatcher does with a started build of either kind.
type JobControl interface {
	Status(ctx context.Context, buildID string) (RunStatus, error)
	// Logs answers the tail of each step's output.
	Logs(ctx context.Context, buildID string) (string, error)
	// Remove deletes the build's Job, pod and Secrets; a build already gone is not an error.
	Remove(ctx context.Context, buildID string) error
	// ForgetCloneCredential deletes the clone credential's Secret once the clone is done.
	ForgetCloneCredential(ctx context.Context, buildID string) error
}

// AppRun is an app build ready to start: the queued build, its concrete builder, and what was minted and read for it.
type AppRun struct {
	Build *Build
	// RunID names this attempt's Job: the build id, then a fresh one for the retry.
	RunID      string
	Builder    string
	CloneURL   string
	CloneToken string
	// Secrets are the project secrets marked build-visible; build arguments stay in Build.App.
	Secrets     map[string]string
	LargestPlan BuildSize
}

// AppRunner runs app builds on the cluster (appbuilds.Runner over the k8s app build runner).
type AppRunner interface {
	JobControl
	StartApp(ctx context.Context, run AppRun) error
	// FailureMessage says which limit or step stopped the build, in the plan's own numbers.
	FailureMessage(status RunStatus, build *Build) string
}

// AppPublish is a finished app build to push and deploy.
type AppPublish struct {
	// RunID is the attempt whose Job holds the image.
	BuildID, RunID, ProjectID, AppID, CommitSHA string
	MaxImageBytes                               int64
}

// AppPublisher takes a finished build's image into the registry and deploys it, answering the deploy's id.
type AppPublisher interface {
	PublishBuild(ctx context.Context, publish AppPublish) (string, error)
}

// BuildFailure is an error that names the failure kind its build records; its message is shown to the user.
type BuildFailure struct {
	Kind FailureKind
	Err  error
}

func (f *BuildFailure) Error() string { return f.Err.Error() }
func (f *BuildFailure) Unwrap() error { return f.Err }

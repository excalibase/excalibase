package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// A project's app builds follow the app_build_* columns of its plan, the image cap in bytes.
func TestAppBuildLimits_ReadTheOrganisationsPlan(t *testing.T) {
	plans := fixedTierConfigs{
		domain.Free:       {AppBuilds: config.AppBuildTierLimits{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 900, Concurrency: 1, MaxImage: "2Gi"}},
		domain.Standard:   {AppBuilds: config.AppBuildTierLimits{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi", TimeoutSeconds: 1800, Concurrency: 2, MaxImage: "5Gi", MaxBuildsPerHour: 40}},
		domain.Enterprise: {AppBuilds: config.AppBuildTierLimits{CPUMillis: 3000, Memory: "16Gi", Disk: "10Gi", TimeoutSeconds: 1800, Concurrency: 2, MaxImage: "5Gi"}},
	}
	limits := NewAppBuildLimits(fixedPlan{tier: domain.Standard}, plans)
	got, err := limits.AppBuildLimits(context.Background(), "p1")
	want := sitebuilds.AppLimits{Limits: sitebuilds.Limits{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi", TimeoutSeconds: 1800,
		ProjectConcurrency: 2}, MaxImageBytes: 5 << 30, MaxBuildsPerHour: 40}
	if err != nil || got != want {
		t.Fatalf("AppBuildLimits = %+v, %v; want %+v", got, err, want)
	}
	largest, err := limits.LargestAppBuild(context.Background())
	if err != nil || largest != (sitebuilds.BuildSize{CPUMillis: 4000, Memory: "16Gi", Disk: "20Gi"}) {
		t.Fatalf("LargestAppBuild = %+v, %v", largest, err)
	}
	for name, source := range map[string]sitebuilds.AppLimitsSource{
		"no plan":     NewAppBuildLimits(fixedPlan{err: ErrOrgTierUnresolved}, plans),
		"no tier row": NewAppBuildLimits(fixedPlan{tier: domain.Enterprise}, fixedTierConfigs{}),
		"no source":   NewAppBuildLimits(nil, plans),
	} {
		if _, err := source.AppBuildLimits(context.Background(), "p1"); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v, want ErrOrgTierUnresolved", name, err)
		}
	}
	broken := fixedTierConfigs{domain.Free: {AppBuilds: config.AppBuildTierLimits{CPUMillis: 1, Memory: "1Gi", Disk: "1Gi", MaxImage: "lots"}}}
	if _, err := NewAppBuildLimits(fixedPlan{tier: domain.Free}, broken).AppBuildLimits(context.Background(), "p1"); err == nil {
		t.Error("an image cap that is no size must refuse the build, never run it uncapped")
	}
	if _, err := NewAppBuildLimits(nil, fixedTierConfigs{}).LargestAppBuild(context.Background()); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Errorf("no tier rows: %v", err)
	}
}

type fakeAppPublisher struct {
	result AppBuildResult
	err    error
}

func (f *fakeAppPublisher) Publish(_ context.Context, result AppBuildResult) (*apphost.Deploy, error) {
	f.result = result
	if f.err != nil {
		return nil, f.err
	}
	return &apphost.Deploy{ID: "deploy-7"}, nil
}

type fakeAppGetter map[string]*apphost.App

func (f fakeAppGetter) Get(projectID, id string) (*apphost.App, error) {
	return f[projectID+"/"+id], nil
}

func TestAppBuildQueuePublisher_FindsTheAppAndNamesEachRefusal(t *testing.T) {
	app := &apphost.App{ID: "app-1", ProjectID: "p1", Name: "api"}
	apps := fakeAppGetter{"p1/app-1": app}
	publish := sitebuilds.AppPublish{BuildID: "b1", RunID: "b1-r1", ProjectID: "p1", AppID: "app-1", CommitSHA: "abc", MaxImageBytes: 1 << 30}
	inner := &fakeAppPublisher{}
	queue := newAppBuildQueuePublisher(inner, apps)
	deployID, err := queue.PublishBuild(context.Background(), publish)
	if err != nil || deployID != "deploy-7" || inner.result != (AppBuildResult{BuildID: "b1-r1", ProjectID: "p1", CommitSHA: "abc", App: app, MaxImage: 1 << 30}) {
		t.Fatalf("published %+v as %q: %v", inner.result, deployID, err)
	}
	cases := map[error]sitebuilds.FailureKind{
		fmt.Errorf("%w (5 bytes)", ErrImageTooLarge):           appbuilds.FailImageTooLarge,
		fmt.Errorf("%w: 9 bytes in use", ErrRegistryFull):      appbuilds.FailRegistryFull,
		fmt.Errorf("%w: a symlink", appregistry.ErrBadArchive): appbuilds.FailBadImage,
		fmt.Errorf("%w: 1 byte free", ErrSpoolFull):            sitebuilds.FailPlatform,
	}
	for refusal, kind := range cases {
		inner.err = refusal
		_, err := queue.PublishBuild(context.Background(), publish)
		var failure *sitebuilds.BuildFailure
		if !errors.As(err, &failure) || failure.Kind != kind || !errors.Is(err, refusal) {
			t.Errorf("%v: %v, want kind %s", refusal, err, kind)
		}
	}
	inner.err = errors.New("dial tcp 10.0.0.9: refused")
	if _, err := queue.PublishBuild(context.Background(), publish); err == nil || errors.As(err, new(*sitebuilds.BuildFailure)) {
		t.Errorf("an unnamed failure stays the dispatcher's to word: %v", err)
	}
	gone := publish
	gone.AppID = "deleted"
	var failure *sitebuilds.BuildFailure
	if _, err := queue.PublishBuild(context.Background(), gone); !errors.As(err, &failure) || failure.Kind != sitebuilds.FailDeploy {
		t.Errorf("an app deleted mid-build: %v", err)
	}
}

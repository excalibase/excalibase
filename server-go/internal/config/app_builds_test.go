package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestLoadAppBuilds_DefaultsToThePinnedImagesAndItsOwnNamespace(t *testing.T) {
	builds := loadAppBuilds()
	if builds.Namespace != "excalibase-app-builds" || builds.BuildKitImage != DefaultAppBuildKitImage || builds.BuilderImage != DefaultAppBuilderImage ||
		builds.RunImage != DefaultAppRunImage || builds.CraneImage != DefaultAppCraneImage {
		t.Fatalf("%+v", builds)
	}
	t.Setenv("APP_BUILD_NAMESPACE", " app-builds ")
	t.Setenv("APP_BUILD_BUILDKIT_IMAGE", "mirror/buildkit@sha256:1")
	t.Setenv("APP_BUILD_BUILDER_IMAGE", "mirror/builder@sha256:2")
	t.Setenv("APP_BUILD_RUN_IMAGE", "mirror/run@sha256:3")
	t.Setenv("APP_BUILD_CRANE_IMAGE", "mirror/crane@sha256:4")
	builds = loadAppBuilds()
	if builds.Namespace != "app-builds" || builds.BuildKitImage != "mirror/buildkit@sha256:1" || builds.BuilderImage != "mirror/builder@sha256:2" ||
		builds.RunImage != "mirror/run@sha256:3" || builds.CraneImage != "mirror/crane@sha256:4" {
		t.Fatalf("%+v", builds)
	}
}

// A namespace that cannot exist stops the boot, but only where app git builds are on.
func TestValidateAppBuilds(t *testing.T) {
	bad := AppConfig{AppBuild: AppBuildConfig{Namespace: "Not_A_Namespace"}}
	if err := bad.validateAppBuilds(); err != nil {
		t.Errorf("flag off: %v", err)
	}
	bad.DarkFeatures = []features.Feature{features.AppGitBuilds}
	if err := bad.validateAppBuilds(); err == nil {
		t.Error("flag on with a bad namespace")
	}
	good := AppConfig{DarkFeatures: bad.DarkFeatures, AppBuild: loadAppBuilds()}
	if err := good.validateAppBuilds(); err != nil {
		t.Errorf("defaults: %v", err)
	}
}

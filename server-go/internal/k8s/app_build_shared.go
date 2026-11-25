package k8s

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// What the single-host runner (internal/dockerbuilds, EXC-677) shares with the
// app build pod: the same steps, scripts and flags, and the same failure names.
const (
	AppBuildStepBuildKit      = appBuildBuildKit
	AppBuildStepShareCheckout = appBuildShareCheckout
	AppBuildStepRunImage      = appBuildRunImage
	AppBuildStepAnalyze       = appBuildAnalyze
	AppBuildStepBuildpacks    = appBuildBuildpacks
	AppBuildStepExport        = appBuildExport
	AppBuildArchivePath       = appBuildArchivePath
	AppBuildSecretsDir        = appBuildSecretsDir
	AppBuildPlatformDir       = appBuildPlatformDir
	AppBuildCNBUID            = int(appBuildCNBUID)
	// AppBuildKitEntry runs as the BuildKit step's shell, with the context and the Dockerfile as its two arguments.
	AppBuildKitEntry = buildKitEntry
	// AppBuildDefaultPlatform is the os/arch the lifecycle reads one image for, unless the operator says otherwise.
	AppBuildDefaultPlatform = defaultAppBuildPlatform
)

// AppBuildKitArgs is buildctl's command line, after the entry script's checks.
func AppBuildKitArgs(spec appbuilds.RunSpec) []string { return buildKitArgs(spec) }

// AppBuildKitEnv is the BuildKit step's fixed variables.
func AppBuildKitEnv() []EnvPair { return buildKitEnv() }

// AppBuildFailureKind names what stopped an app build from the step that exited and its code.
func AppBuildFailureKind(step string, code int32) sitebuilds.FailureKind {
	return appStepFailureKind(step, code)
}

// AppBuildCollectScript is the step that hands the image on as one archive.
func AppBuildCollectScript() (string, error) {
	body, err := appBuildScripts.ReadFile("app_build_scripts/collect.sh")
	if err != nil {
		return "", fmt.Errorf("read build script collect.sh: %w", err)
	}
	return string(body), nil
}

// AppBuildImageDir is where the Buildpacks export leaves the image inside its layout.
func AppBuildImageDir() string { return appBuildLayoutDir + "/" + layoutPath(appBuildImageRef) }

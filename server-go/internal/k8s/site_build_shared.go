package k8s

import (
	"fmt"
	"path"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// What the single-host runner (internal/dockerbuilds, EXC-671) shares with the
// build Job, so both run the same scripts with the same variables and name
// failures the same way.
const (
	SiteBuildStepClone   = siteBuildClone
	SiteBuildStepBuild   = siteBuildBuild
	SiteBuildStepCollect = siteBuildCollect
	SiteBuildArchivePath = siteBuildArchivePath
	SiteBuildReadyPath   = siteBuildCollectReady
)

// EnvPair is one environment variable.
type EnvPair struct{ Name, Value string }

// SiteBuildScript is one of the build scripts, by file name.
func SiteBuildScript(name string) (string, error) {
	if path.Base(name) != name {
		return "", fmt.Errorf("build script %q is not a file name", name)
	}
	body, err := siteBuildScripts.ReadFile("site_build_scripts/" + name)
	if err != nil {
		return "", fmt.Errorf("read build script %s: %w", name, err)
	}
	return string(body), nil
}

// SiteBuildStepEnv is the build step's fixed variables. Each runner sets them
// after the declared ones, so a declared variable cannot move HOME or the commands.
func SiteBuildStepEnv(spec sitebuilds.RunSpec) []EnvPair {
	return []EnvPair{
		{"HOME", "/work/home"}, {"npm_config_cache", "/work/npm-cache"},
		{"COREPACK_HOME", "/work/corepack"}, {"COREPACK_ENABLE_DOWNLOAD_PROMPT", "0"},
		{"CI", "true"}, {"NEXT_TELEMETRY_DISABLED", "1"}, {"ASTRO_TELEMETRY_DISABLED", "1"},
		{"ROOT_DIR", spec.RootDir}, {"INSTALL_COMMAND", spec.InstallCommand},
		{"BUILD_COMMAND", spec.BuildCommand},
	}
}

// SiteBuildFailureKind names what stopped a build from the step that exited and its code.
func SiteBuildFailureKind(step string, code int32) sitebuilds.FailureKind {
	return stepFailureKind(step, code)
}

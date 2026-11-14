package dockerbuilds

import (
	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The app build steps, named as the app build pod names them so both read failures the same way.
const (
	stepBuildKit = k8s.AppBuildStepBuildKit
	stepShare    = k8s.AppBuildStepShareCheckout
	stepRunImage = k8s.AppBuildStepRunImage
	stepAnalyze  = k8s.AppBuildStepAnalyze
	stepPacks    = k8s.AppBuildStepBuildpacks
	stepExport   = k8s.AppBuildStepExport
)

// allAppSteps is every step either builder makes, so Remove need not know which one ran.
var allAppSteps = []string{stepPrepare, stepClone, stepBuildKit, stepShare, stepRunImage, stepAnalyze, stepPacks, stepExport, stepCollect}

var dockerfilePlan = appPlan([]string{stepPrepare, stepClone, stepBuildKit, stepCollect}, stepBuildKit)

var buildpacksPlan = appPlan([]string{stepPrepare, stepClone, stepShare, stepRunImage, stepAnalyze, stepPacks, stepExport, stepCollect}, stepPacks)

func planFor(builder appbuilds.Builder) stepPlan {
	if builder == appbuilds.BuilderDockerfile {
		return dockerfilePlan
	}
	return buildpacksPlan
}

// appPlan is an app build's steps; the clone, the image fetch and the step running the app's code go out through the sidecar.
func appPlan(chain []string, heavy string) stepPlan {
	return stepPlan{
		chain:       chain,
		online:      map[string]bool{stepClone: true, stepRunImage: true, heavy: true},
		limited:     map[string]bool{heavy: true},
		failureKind: k8s.AppBuildFailureKind,
		logs: []logStep{{stepEgress, "network"}, {stepClone, "clone"}, {stepBuildKit, "docker build"}, {stepRunImage, "run image"},
			{stepAnalyze, "analyze"}, {stepPacks, "buildpacks"}, {stepExport, "export"}, {stepCollect, "collect"}},
		archive: k8s.AppBuildArchivePath,
	}
}

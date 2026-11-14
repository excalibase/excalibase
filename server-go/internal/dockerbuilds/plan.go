package dockerbuilds

import (
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// stepPlan is what a kind of build runs: its steps in order, each started once the one before exits 0,
// and how Status, Logs and Collect read them.
type stepPlan struct {
	chain []string
	// online are the steps that go out through the sidecar, so its death fails the build.
	online map[string]bool
	// limited are the steps sized by the plan, where a SIGKILL is the memory limit.
	limited     map[string]bool
	failureKind func(step string, code int32) sitebuilds.FailureKind
	logs        []logStep
	// archive is the packed output's path in the collect step.
	archive string
}

// logStep is a step whose output the history keeps; the sidecar's refusals come first.
type logStep struct{ step, title string }

var sitePlan = stepPlan{
	chain:       []string{stepPrepare, stepClone, stepBuild, stepCollect},
	online:      map[string]bool{stepClone: true, stepBuild: true},
	limited:     map[string]bool{stepBuild: true},
	failureKind: k8s.SiteBuildFailureKind,
	logs:        []logStep{{stepEgress, "network"}, {stepClone, "clone"}, {stepBuild, "build"}, {stepCollect, "collect"}},
	archive:     k8s.SiteBuildArchivePath,
}

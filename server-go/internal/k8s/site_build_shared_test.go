package k8s

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func TestSiteBuildScriptIsTheJobsOwn(t *testing.T) {
	for _, name := range []string{"clone.sh", "build.sh", "collect.sh"} {
		body, err := SiteBuildScript(name)
		if err != nil || !strings.HasPrefix(body, "#!/bin/bash") {
			t.Fatalf("%s: %v %q", name, err, body)
		}
	}
	if _, err := SiteBuildScript("../site_build_job.go"); err == nil {
		t.Fatal("a name outside the scripts was read")
	}
}

// The single-host runner takes the build step's fixed variables from here, so both runners give a build the same.
func TestSiteBuildStepEnvMatchesTheJob(t *testing.T) {
	spec := sitebuilds.RunSpec{RootDir: "web", InstallCommand: "npm ci", BuildCommand: "npm run build", Memory: "1Gi", Disk: "2Gi"}
	job := map[string]string{}
	for _, env := range buildContainer(spec).Env {
		job[env.Name] = env.Value
	}
	shared := SiteBuildStepEnv(spec)
	if len(shared) != len(job) {
		t.Fatalf("shared %v, job %v", shared, job)
	}
	for _, pair := range shared {
		if job[pair.Name] != pair.Value {
			t.Fatalf("%s: shared %q, job %q", pair.Name, pair.Value, job[pair.Name])
		}
	}
}

func TestSiteBuildFailureKindNamesSteps(t *testing.T) {
	cases := []struct {
		step string
		code int32
		want sitebuilds.FailureKind
	}{
		{SiteBuildStepClone, 1, sitebuilds.FailClone},
		{SiteBuildStepBuild, 10, sitebuilds.FailInstall},
		{SiteBuildStepBuild, 11, sitebuilds.FailBuild},
		{SiteBuildStepBuild, 12, sitebuilds.FailRootDir},
		{SiteBuildStepCollect, 3, sitebuilds.FailNoOutput},
		{"prepare-workdir", 1, sitebuilds.FailPlatform},
	}
	for _, c := range cases {
		if got := SiteBuildFailureKind(c.step, c.code); got != c.want {
			t.Fatalf("%s exit %d: %s, want %s", c.step, c.code, got, c.want)
		}
	}
	if SiteBuildArchivePath != siteBuildArchivePath || SiteBuildReadyPath != siteBuildCollectReady {
		t.Fatal("the shared paths moved away from the Job's")
	}
}

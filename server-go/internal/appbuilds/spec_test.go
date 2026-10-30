package appbuilds

import (
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func validSpec() RunSpec {
	return RunSpec{
		BuildID: "b1", AppID: "app-1", ProjectID: "proj-1", Builder: BuilderDockerfile,
		CloneURL: "https://github.com/acme/api.git", Commit: strings.Repeat("a", 40), CloneToken: "t",
		CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", Timeout: 15 * time.Minute, MaxImageBytes: 2 << 30,
		LargestPlan: sitebuilds.BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"},
	}
}

func TestRunSpec_ValidAcceptsBothBuilders(t *testing.T) {
	for _, builder := range []Builder{BuilderDockerfile, BuilderBuildpacks} {
		spec := validSpec()
		spec.Builder = builder
		if err := spec.Validate(); err != nil {
			t.Errorf("%s: %v", builder, err)
		}
	}
}

// The renderer sees only what Validate let through: no auto builder, no path
// that leaves the checkout, no size the quota maths would panic on.
func TestRunSpec_ValidateRefuses(t *testing.T) {
	cases := map[string]func(*RunSpec){
		"auto is resolved before a run":  func(s *RunSpec) { s.Builder = "auto" },
		"unknown builder":                func(s *RunSpec) { s.Builder = "railpack" },
		"root dir leaves the checkout":   func(s *RunSpec) { s.RootDir = "../etc" },
		"absolute root dir":              func(s *RunSpec) { s.RootDir = "/etc" },
		"dockerfile leaves the checkout": func(s *RunSpec) { s.DockerfilePath = "../../Dockerfile" },
		"memory is not a size":           func(s *RunSpec) { s.Memory = "lots" },
		"largest plan disk not a size":   func(s *RunSpec) { s.LargestPlan.Disk = "" },
		"commit is not a sha":            func(s *RunSpec) { s.Commit = "main; rm -rf /" },
		"no image size cap":              func(s *RunSpec) { s.MaxImageBytes = 0 },
		"build arg name not a name":      func(s *RunSpec) { s.BuildArgs = map[string]string{"A B": "1"} },
		"secret name not a name":         func(s *RunSpec) { s.Secrets = map[string]string{"../x": "1"} },
		"reserved BuildKit argument":     func(s *RunSpec) { s.BuildArgs = map[string]string{"BUILDKIT_SYNTAX": "evil/frontend"} },
		"oversized inputs":               func(s *RunSpec) { s.Secrets = map[string]string{"BIG": strings.Repeat("x", 300<<10)} },
		"build id not a label":           func(s *RunSpec) { s.BuildID = "Build_1" },
		"no time limit":                  func(s *RunSpec) { s.Timeout = 0 },
		"no cpu":                         func(s *RunSpec) { s.CPUMillis = 0 },
		"plain http clone":               func(s *RunSpec) { s.CloneURL = "http://github.com/acme/api.git" },
		"git daemon by default":          func(s *RunSpec) { s.CloneURL = "git://gitsrv/site.git" },
		"clone url as an option":         func(s *RunSpec) { s.CloneURL = "--upload-pack=touch /tmp/x" },
		"file clone":                     func(s *RunSpec) { s.CloneURL = "file:///etc" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestRunSpec_ValidateTakesNamedCloneSchemes(t *testing.T) {
	spec := validSpec()
	spec.CloneURL = "git://gitsrv.gitsrv.svc.cluster.local/site.git"
	if err := spec.Validate("git"); err != nil {
		t.Errorf("a named scheme: %v", err)
	}
}

func TestDockerfileDefaultsToTheRootDirectory(t *testing.T) {
	spec := validSpec()
	spec.RootDir = "services/api"
	if got := spec.ContextDir(); got != "/work/src/services/api" {
		t.Errorf("context %q", got)
	}
	if got := spec.DockerfileFile(); got != "/work/src/services/api/Dockerfile" {
		t.Errorf("dockerfile %q", got)
	}
	spec.DockerfilePath = "deploy/api.Dockerfile"
	if got := spec.DockerfileFile(); got != "/work/src/services/api/deploy/api.Dockerfile" {
		t.Errorf("dockerfile %q", got)
	}
}

// A failure says which limit or step stopped the build in the plan's own numbers.
func TestFailureMessage(t *testing.T) {
	spec := validSpec()
	cases := map[sitebuilds.FailureKind]string{
		sitebuilds.FailOutOfMemory: "4Gi",
		sitebuilds.FailTimeout:     "15 minutes",
		sitebuilds.FailDisk:        "10Gi",
		FailImageTooLarge:          "2.0 GiB",
		sitebuilds.FailBuild:       "exit 2",
		FailNoBuildpack:            "no buildpack",
		FailNoDockerfile:           "no Dockerfile",
		sitebuilds.FailClone:       "could not be cloned",
		sitebuilds.FailFence:       "network fence",
	}
	for kind, want := range cases {
		got := FailureMessage(sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: kind, ExitCode: 2}, spec)
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q lacks %q", kind, got, want)
		}
	}
}

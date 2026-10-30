package sitebuilds

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

func TestPreviewAllowedFollowsModeAndForks(t *testing.T) {
	cases := []struct {
		name     string
		settings PreviewSettings
		source   string
		fork     bool
		want     bool
	}{
		{"off makes none", PreviewSettings{}, sites.PreviewSourceBranch, false, false},
		{"pull requests: a same-repository one", PreviewSettings{Mode: PreviewModePullRequests}, sites.PreviewSourcePullRequest, false, true},
		{"pull requests: no branch", PreviewSettings{Mode: PreviewModePullRequests}, sites.PreviewSourceBranch, false, false},
		{"pull requests: a fork only with forks", PreviewSettings{Mode: PreviewModePullRequests}, sites.PreviewSourcePullRequest, true, false},
		{"pull requests: a fork with forks", PreviewSettings{Mode: PreviewModePullRequests, PreviewAccess: PreviewAccess{Forks: true}}, sites.PreviewSourcePullRequest, true, true},
		{"branches: a branch", PreviewSettings{Mode: PreviewModeBranches}, sites.PreviewSourceBranch, false, true},
		{"branches: a same-repository pull request is its branch", PreviewSettings{Mode: PreviewModeBranches}, sites.PreviewSourcePullRequest, false, false},
		{"branches: a fork with forks", PreviewSettings{Mode: PreviewModeBranches, PreviewAccess: PreviewAccess{Forks: true}}, sites.PreviewSourcePullRequest, true, true},
	}
	for _, tc := range cases {
		if got := previewAllowed(tc.settings, tc.source, tc.fork); got != tc.want {
			t.Errorf("%s: allowed = %v", tc.name, got)
		}
	}
}

func TestNarrowKeepsOnlyWhatIsStillGranted(t *testing.T) {
	build := &PreviewBuild{Source: sites.PreviewSourceBranch, Secrets: true, APIAccess: true, PublicLinks: true}
	current := PreviewSettings{Mode: PreviewModeBranches, PreviewAccess: PreviewAccess{APIAccess: true}}
	if reason := build.narrow(current); reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if build.Secrets || !build.APIAccess || build.PublicLinks {
		t.Fatalf("build = %+v; want only API access left", build)
	}
	fork := &PreviewBuild{Source: sites.PreviewSourcePullRequest, FromFork: true}
	if reason := fork.narrow(PreviewSettings{Mode: PreviewModePullRequests}); !strings.Contains(reason, "turned off") {
		t.Fatalf("a fork build after forks were turned off: reason = %q", reason)
	}
}

func TestPublicFailureNeverCarriesDetail(t *testing.T) {
	for _, kind := range []FailureKind{FailInstall, FailBuild, FailTimeout, FailOutOfMemory, FailDisk, FailNoOutput, FailRootDir, FailImage, FailPlatform, FailDeploy} {
		message := publicFailure(kind)
		if message == "" || !strings.Contains(message, "Studio") {
			t.Errorf("%s: %q", kind, message)
		}
	}
}

// Package appbuilds builds an app's image from a git commit under gVisor
// (EXC-547, ADR 0037 phase 2). It reuses the site build pipeline's statuses and
// failure kinds; the build itself makes an OCI image instead of a folder.
package appbuilds

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// Builder is how a commit becomes an image.
type Builder string

const (
	// BuilderDockerfile builds the repository's Dockerfile.
	BuilderDockerfile Builder = "dockerfile"
	// BuilderBuildpacks detects the language with Cloud Native Buildpacks (Paketo).
	BuilderBuildpacks Builder = "buildpacks"
)

// Failure kinds only image builds have.
const (
	FailImageTooLarge sitebuilds.FailureKind = "image-size"
	FailNoBuildpack   sitebuilds.FailureKind = "no-buildpack"
	FailNoDockerfile  sitebuilds.FailureKind = "no-dockerfile"
)

// WorkSource is where the clone step checks the commit out.
const WorkSource = "/work/src"

// RunSpec is one app build as the cluster runs it.
type RunSpec struct {
	BuildID   string
	AppID     string
	ProjectID string
	// Builder is concrete here: "auto" is resolved against the commit before a run.
	Builder    Builder
	CloneURL   string
	Commit     string
	CloneToken string
	// RootDir is the build context inside the repository; "" is its root.
	RootDir string
	// DockerfilePath is relative to RootDir; "" is RootDir/Dockerfile.
	DockerfilePath string
	// BuildArgs are plain, non-secret values; they end up in the image's history.
	BuildArgs map[string]string
	// Secrets are the project secrets marked build-visible: files for BuildKit's
	// secret mounts, or the Buildpacks platform env. Any step of the build can read them.
	Secrets   map[string]string
	CPUMillis int
	Memory    string
	Disk      string
	Timeout   time.Duration
	// MaxImageBytes caps the OCI archive the build hands on.
	MaxImageBytes int64
	// LargestPlan sizes the namespace quota backstop.
	LargestPlan sitebuilds.BuildSize
}

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	buildIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,50}[a-z0-9])?$`)
)

// maxBuildInputBytes bounds the build args and secrets together: both end up in a Secret or the pod spec.
const maxBuildInputBytes = 256 << 10

// Validate refuses what the renderer must never see. The clone URL must be
// https unless the caller names other schemes (a test's git daemon).
func (s RunSpec) Validate(cloneSchemes ...string) error {
	if s.Builder != BuilderDockerfile && s.Builder != BuilderBuildpacks {
		return fmt.Errorf("unknown builder %q", s.Builder)
	}
	if !buildIDPattern.MatchString(s.BuildID) {
		return fmt.Errorf("the build id %q is not a DNS label", s.BuildID)
	}
	if !commitPattern.MatchString(s.Commit) {
		return errors.New("the commit must be a full sha")
	}
	if err := checkCloneURL(s.CloneURL, cloneSchemes); err != nil {
		return err
	}
	if s.CPUMillis <= 0 || s.Timeout <= 0 {
		return errors.New("the build needs CPU and a time limit")
	}
	for label, relative := range map[string]string{"root directory": s.RootDir, "Dockerfile path": s.DockerfilePath} {
		if !insideCheckout(relative) {
			return fmt.Errorf("the %s %q leaves the repository", label, relative)
		}
	}
	for label, value := range map[string]string{"memory": s.Memory, "disk": s.Disk,
		"largest plan's memory": s.LargestPlan.Memory, "largest plan's disk": s.LargestPlan.Disk} {
		if _, err := resource.ParseQuantity(value); err != nil {
			return fmt.Errorf("the build's %s %q is not a size: %w", label, value, err)
		}
	}
	if s.MaxImageBytes <= 0 {
		return errors.New("the build has no image size cap")
	}
	return checkInputs(s.BuildArgs, s.Secrets)
}

func checkCloneURL(raw string, schemes []string) error {
	if len(schemes) == 0 {
		schemes = []string{"https"}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || strings.HasPrefix(raw, "-") || !slices.Contains(schemes, parsed.Scheme) {
		return fmt.Errorf("the clone URL must be %s with a host", strings.Join(schemes, " or "))
	}
	return nil
}

// checkInputs refuses names that are not variables, BuildKit's own reserved arguments, and oversized sets.
func checkInputs(buildArgs, secrets map[string]string) error {
	size := 0
	for kind, values := range map[string]map[string]string{"build argument": buildArgs, "secret": secrets} {
		for name, value := range values {
			if !namePattern.MatchString(name) {
				return fmt.Errorf("the %s name %q is not a variable name", kind, name)
			}
			if strings.HasPrefix(strings.ToUpper(name), "BUILDKIT_") {
				return fmt.Errorf("the %s %q is reserved for the builder", kind, name)
			}
			size += len(name) + len(value)
		}
	}
	if size > maxBuildInputBytes {
		return fmt.Errorf("the build arguments and secrets come to %d bytes, more than %d", size, maxBuildInputBytes)
	}
	return nil
}

func insideCheckout(relative string) bool {
	if relative == "" {
		return true
	}
	if strings.HasPrefix(relative, "/") || strings.ContainsRune(relative, 0) {
		return false
	}
	cleaned := path.Clean(relative)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

// ContextDir is the build context in the pod.
func (s RunSpec) ContextDir() string {
	if s.RootDir == "" {
		return WorkSource
	}
	return path.Join(WorkSource, path.Clean(s.RootDir))
}

// DockerfileFile is the Dockerfile in the pod.
func (s RunSpec) DockerfileFile() string {
	if s.DockerfilePath == "" {
		return path.Join(s.ContextDir(), "Dockerfile")
	}
	return path.Join(s.ContextDir(), path.Clean(s.DockerfilePath))
}

// FailureMessage says which limit or step stopped a build, in the plan's own numbers.
func FailureMessage(status sitebuilds.RunStatus, spec RunSpec) string {
	switch status.Failure {
	case sitebuilds.FailOutOfMemory:
		return fmt.Sprintf("out of memory: the build needed more than its %s of memory", spec.Memory)
	case sitebuilds.FailTimeout:
		return fmt.Sprintf("time limit: the build ran longer than %s", minutes(spec.Timeout))
	case sitebuilds.FailDisk:
		return fmt.Sprintf("disk limit: the build wrote more than its %s of disk", spec.Disk)
	case FailImageTooLarge:
		return fmt.Sprintf("the built image is larger than the %.1f GiB your plan allows", float64(spec.MaxImageBytes)/(1<<30))
	case FailNoBuildpack:
		return "no buildpack recognised the app: add a Dockerfile, or a package.json, go.mod, requirements.txt, pom.xml or similar"
	case FailNoDockerfile:
		return "no Dockerfile at " + strings.TrimPrefix(spec.DockerfileFile(), WorkSource+"/")
	case sitebuilds.FailClone:
		return "the repository could not be cloned at this commit; check the GitHub App still covers it"
	case sitebuilds.FailFence:
		return "the build's network fence did not come up, so nothing was run; build again"
	case sitebuilds.FailBuild:
		return fmt.Sprintf("the image build failed (exit %d); see the log", status.ExitCode)
	case sitebuilds.FailImage:
		return "a builder image could not be pulled: " + status.Detail
	default:
		return "the build could not run: " + status.Detail
	}
}

func minutes(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return fmt.Sprintf("%d seconds", int(d/time.Second))
}

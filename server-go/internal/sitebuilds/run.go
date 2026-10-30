package sitebuilds

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrOutputTooLarge is packed build output past what a deploy of the project may hold.
var ErrOutputTooLarge = errors.New("the packed build output is larger than this project's deploys may be")

// RunSpec is one build as the cluster runs it.
type RunSpec struct {
	BuildID   string
	SiteID    string
	ProjectID string
	// Image is the Node image the build runs in, from an allowlisted major.
	Image      string
	CloneURL   string
	Commit     string
	CloneToken string
	RootDir    string
	OutputDir  string
	// InstallCommand and BuildCommand are empty for the defaults the lockfile picks.
	InstallCommand string
	BuildCommand   string
	CPUMillis      int
	Memory         string
	Disk           string
	Timeout        time.Duration
	// Env holds the site's declared variables and the project secrets marked build-visible, nothing else.
	Env map[string]string
	// LargestPlan is the biggest build any plan runs now; the cluster's quota backstop is sized by it.
	LargestPlan BuildSize
}

// BuildSize is one build's CPU, memory and disk.
type BuildSize struct {
	CPUMillis int
	Memory    string
	Disk      string
}

// RunPhase is where a build's pod is.
type RunPhase int

const (
	RunPending RunPhase = iota
	RunRunning
	// RunReady has the output packed and waiting to be collected.
	RunReady
	RunFailed
)

// FailureKind names which limit or stage stopped a build.
type FailureKind string

const (
	FailOutOfMemory FailureKind = "out-of-memory"
	FailTimeout     FailureKind = "time-limit"
	FailDisk        FailureKind = "disk-limit"
	FailFence       FailureKind = "network-fence"
	FailClone       FailureKind = "clone"
	FailRootDir     FailureKind = "root-directory"
	FailInstall     FailureKind = "install"
	FailBuild       FailureKind = "build"
	FailNoOutput    FailureKind = "no-output"
	FailImage       FailureKind = "image"
	FailPlatform    FailureKind = "platform"
	// FailRateLimited is a push past the project's deploys per hour, never started.
	FailRateLimited FailureKind = "rate-limit"
	// FailDeploy is output the deploy path refused or could not publish.
	FailDeploy FailureKind = "deploy"
)

// RunStatus is read from the pod's status, never from an exit code alone:
// under gVisor an out-of-memory kill can leave the container exiting 0.
type RunStatus struct {
	Phase   RunPhase
	Failure FailureKind
	// ExitCode is the failing step's, when it exited on its own.
	ExitCode int32
	Detail   string
	// CloneDone is the clone step finished; its credential is no longer needed.
	CloneDone bool
	// StartFailed is a step the sandbox could not start (runsc's StartError): not the build's fault, worth one retry.
	StartFailed bool
}

// Runner runs builds on the cluster.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) error
	Status(ctx context.Context, buildID string) (RunStatus, error)
	// Collect streams the packed output (tar.gz) into w, failing past limit bytes.
	Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error
	// Logs answers the tail of each step's output.
	Logs(ctx context.Context, buildID string) (string, error)
	// Remove deletes the build's Job, pod and Secrets; a build already gone is not an error.
	Remove(ctx context.Context, buildID string) error
}

// LimitsSource answers a project's build limits from its plan, and the largest build any plan runs.
type LimitsSource interface {
	BuildLimits(ctx context.Context, projectID string) (Limits, error)
	LargestBuild(ctx context.Context) (BuildSize, error)
}

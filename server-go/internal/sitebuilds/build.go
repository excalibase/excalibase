package sitebuilds

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Build statuses.
const (
	StatusQueued    = "QUEUED"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"
)

// Build triggers.
const (
	TriggerPush    = "push"
	TriggerRebuild = "rebuild"
)

var (
	ErrNotConnected  = errors.New("this site has no repository connected")
	ErrBuildNotFound = errors.New("build not found")
	// ErrUnavailable is an install without git builds switched on and wired.
	ErrUnavailable = errors.New("git builds are not available on this install")
	// ErrSingleHost is a single-host install, where git builds are not offered yet.
	ErrSingleHost = errors.New("git builds are not offered on single-host installs yet: build the site in your own CI " +
		"and deploy the output (Studio → Sites → Deploy from CI, or the get_ci_snippet tool with kind site)")
	// ErrNotAuthorized is a repository the caller's own git-host sign-in did not show them.
	ErrNotAuthorized = errors.New("sign in to GitHub from this site first: your sign-in has to show you can read that repository")
	ErrStateInvalid  = errors.New("that sign-in link has expired or was already used; start again from the site")
	// ErrConnectionInactive is a connection whose connector can no longer deploy the project.
	ErrConnectionInactive = errors.New("the member who connected this repository can no longer deploy this project; " +
		"a developer has to sign in with GitHub and connect it again")
)

// Connection is a site's repository and how to build it.
type Connection struct {
	SiteID         string   `json:"siteId"`
	ProjectID      string   `json:"projectId"`
	Provider       string   `json:"provider"`
	InstallationID int64    `json:"installationId"`
	RepositoryID   int64    `json:"repositoryId"`
	Repository     string   `json:"repository"`
	Settings       Settings `json:"settings"`
	ConnectedBy    string   `json:"connectedBy"`
	// ConnectedByName is the connector's username, filled when read for Studio.
	ConnectedByName string `json:"connectedByName,omitempty"`
	// InactiveReason is set once the connector lost the developer rung; nothing builds until a developer reconnects.
	InactiveReason string    `json:"inactiveReason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Limits are a build's plan limits, fixed when it is queued.
type Limits struct {
	CPUMillis          int    `json:"cpuMillis"`
	Memory             string `json:"memory"`
	Disk               string `json:"disk"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	ProjectConcurrency int    `json:"-"`
}

// Build is one queued or finished build of a site.
type Build struct {
	ID            string      `json:"id"`
	SiteID        string      `json:"siteId"`
	ProjectID     string      `json:"projectId"`
	Status        string      `json:"status"`
	Trigger       string      `json:"trigger"`
	CommitSHA     string      `json:"commitSha"`
	CommitMessage string      `json:"commitMessage,omitempty"`
	Repository    string      `json:"repository"`
	Settings      Settings    `json:"settings"`
	Limits        Limits      `json:"limits"`
	DeliveryID    string      `json:"-"`
	DeployID      string      `json:"deployId,omitempty"`
	FailureKind   FailureKind `json:"failureKind,omitempty"`
	FailureReason string      `json:"failureReason,omitempty"`
	// Log is read with one build, never with the list.
	Log         string     `json:"log,omitempty"`
	RequestedBy string     `json:"requestedBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`

	// The connection's host and ids when queued, so the run reaches the same repository.
	Provider       string `json:"provider"`
	InstallationID int64  `json:"-"`
	RepositoryID   int64  `json:"-"`
}

// buildSnapshot is the connection as a build keeps it in its settings column.
type buildSnapshot struct {
	Settings
	Provider       string `json:"provider"`
	InstallationID int64  `json:"installationId"`
	RepositoryID   int64  `json:"repositoryId"`
	Repository     string `json:"repository"`
}

const (
	maxLabelLength   = 200
	shortSHALength   = 7
	minRedactedValue = 4
)

// DeployLabel marks the deploy a build makes: short sha, branch and the commit's first line.
func (b *Build) DeployLabel() string {
	sha := b.CommitSHA
	if len(sha) > shortSHALength {
		sha = sha[:shortSHALength]
	}
	label := sha + " " + b.Settings.Branch
	if subject, _, _ := strings.Cut(b.CommitMessage, "\n"); strings.TrimSpace(subject) != "" {
		label += ": " + strings.TrimSpace(subject)
	}
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, label)
	for len(label) > maxLabelLength {
		runes := []rune(label)
		label = string(runes[:len(runes)-1])
	}
	return label
}

// redact hides each secret value from a log, longest first so a value inside another goes too.
func redact(text string, secrets map[string]string) string {
	names := make([]string, 0, len(secrets))
	for name, value := range secrets {
		if len(value) >= minRedactedValue {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(secrets[names[i]]) > len(secrets[names[j]]) })
	for _, name := range names {
		text = strings.ReplaceAll(text, secrets[name], "[redacted "+name+"]")
	}
	// A tail cut inside a value leaves its end right after the cut mark.
	for _, name := range names {
		value := secrets[name]
		for start := 1; len(value)-start >= minRedactedSuffix; start++ {
			text = strings.ReplaceAll(text, logCutMark+value[start:], logCutMark+"[redacted "+name+"]")
		}
	}
	return text
}

const (
	logCutMark        = "…"
	minRedactedSuffix = 4
)

// failureMessage says which limit or step stopped a build, in the plan's own numbers.
func failureMessage(status RunStatus, limits Limits, settings Settings) string {
	switch status.Failure {
	case FailOutOfMemory:
		return fmt.Sprintf("out of memory: the build needed more than its %s of memory", limits.Memory)
	case FailTimeout:
		return fmt.Sprintf("time limit: the build ran longer than %s", minutes(limits.TimeoutSeconds))
	case FailDisk:
		return fmt.Sprintf("disk limit: the build wrote more than its %s of disk", limits.Disk)
	case FailFence:
		return "the build's network fence did not come up, so nothing was run; build again"
	case FailClone:
		return "the repository could not be cloned at this commit; check the GitHub App still covers it"
	case FailRootDir:
		return fmt.Sprintf("the root directory %q is not in the repository", settings.RootDir)
	case FailInstall:
		return fmt.Sprintf("the install command failed (exit %d); see the log", status.ExitCode)
	case FailBuild:
		return fmt.Sprintf("the build command failed (exit %d); see the log", status.ExitCode)
	case FailNoOutput:
		return fmt.Sprintf("the build finished but wrote no %q directory; check the output directory", settings.OutputDir)
	case FailImage:
		return "the Node image for this build could not be pulled: " + status.Detail
	default:
		return "the build could not run: " + status.Detail
	}
}

func minutes(seconds int) string {
	if seconds%60 == 0 {
		return fmt.Sprintf("%d minutes", seconds/60)
	}
	return fmt.Sprintf("%d seconds", seconds)
}

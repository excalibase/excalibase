// Package sitebuilds deploys a site on a git push (EXC-354, ADR 0043
// decision 8): a site connects one repository and branch; a push to it
// queues a build; a dispatcher runs queued builds as sandboxed Jobs within
// the install's and each project's caps, collects the output and deploys it
// through the same path an upload takes.
package sitebuilds

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	DefaultNodeVersion = "22"
	DefaultOutputDir   = "dist"
	MaxEnvVars         = 50
	MaxEnvValueBytes   = 4096
	maxEnvTotalBytes   = 64 << 10
	maxCommandLength   = 1024
	maxPathLength      = 256
	maxBranchLength    = 255
)

// NodeVersions are the Node majors a build may run on; each is an image tag.
var NodeVersions = []string{"20", "22", "24"}

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// reservedEnv are set by the build itself; a declared variable may not shadow them.
	reservedEnv = map[string]bool{
		"HOME": true, "PATH": true, "CI": true, "npm_config_cache": true, "COREPACK_HOME": true,
		"COREPACK_ENABLE_DOWNLOAD_PROMPT": true, "ROOT_DIR": true, "OUTPUT_DIR": true, "INSTALL_COMMAND": true,
		"BUILD_COMMAND": true, "NEXT_TELEMETRY_DISABLED": true, "ASTRO_TELEMETRY_DISABLED": true,
	}
)

// Settings are how a site's repository is built.
type Settings struct {
	Branch         string            `json:"branch"`
	RootDir        string            `json:"rootDir"`
	InstallCommand string            `json:"installCommand"`
	BuildCommand   string            `json:"buildCommand"`
	OutputDir      string            `json:"outputDir"`
	NodeVersion    string            `json:"nodeVersion"`
	Env            map[string]string `json:"env"`
}

// Normalise trims and checks the settings, answering an InputError for anything refused.
func (s Settings) Normalise() (Settings, error) {
	out := s
	out.Branch = strings.TrimSpace(s.Branch)
	out.InstallCommand = strings.TrimSpace(s.InstallCommand)
	out.BuildCommand = strings.TrimSpace(s.BuildCommand)
	if strings.TrimSpace(s.NodeVersion) == "" {
		out.NodeVersion = DefaultNodeVersion
	} else {
		out.NodeVersion = strings.TrimSpace(s.NodeVersion)
	}
	var err error
	if out.RootDir, err = cleanRelative("root directory", s.RootDir, true); err != nil {
		return Settings{}, err
	}
	if out.OutputDir, err = cleanRelative("output directory", s.OutputDir, false); err != nil {
		return Settings{}, err
	}
	checks := []error{validBranch(out.Branch), validNode(out.NodeVersion),
		validCommand("install command", out.InstallCommand), validCommand("build command", out.BuildCommand), validEnv(s.Env)}
	if err := errors.Join(checks...); err != nil {
		return Settings{}, &InputError{Reason: err.Error()}
	}
	out.Env = make(map[string]string, len(s.Env))
	for name, value := range s.Env {
		out.Env[name] = value
	}
	return out, nil
}

// cleanRelative keeps a path inside the repository: relative, no "..", no control characters.
func cleanRelative(what, raw string, emptyAllowed bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." || trimmed == "./" {
		if emptyAllowed {
			return "", nil
		}
		return "", &InputError{Reason: fmt.Sprintf("the %s is required and must be a folder inside the repository, such as dist", what)}
	}
	if len(trimmed) > maxPathLength || strings.HasPrefix(trimmed, "/") || hasControl(trimmed) || strings.Contains(trimmed, "\\") {
		return "", &InputError{Reason: fmt.Sprintf("the %s must be a relative path inside the repository", what)}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(trimmed, "..") {
		return "", &InputError{Reason: fmt.Sprintf("the %s may not leave the repository", what)}
	}
	return cleaned, nil
}

func hasControl(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// validBranch accepts the branch names git does, minus anything a shell or a ref spec would read twice.
func validBranch(branch string) error {
	switch {
	case branch == "":
		return errors.New("a branch is required")
	case len(branch) > maxBranchLength || !branchPattern.MatchString(branch):
		return errors.New("a branch name may use letters, digits, '.', '_', '-' and '/' only")
	case strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasSuffix(branch, ".lock"):
		return errors.New("that is not a valid branch name")
	}
	return nil
}

func validNode(version string) error {
	for _, known := range NodeVersions {
		if version == known {
			return nil
		}
	}
	return fmt.Errorf("Node %s is not offered; choose one of %s", version, strings.Join(NodeVersions, ", "))
}

func validCommand(what, command string) error {
	if len(command) > maxCommandLength {
		return fmt.Errorf("the %s is at most %d characters", what, maxCommandLength)
	}
	if hasControl(command) {
		return fmt.Errorf("the %s must be one line", what)
	}
	return nil
}

func validEnv(env map[string]string) error {
	return validVariables("build variable", env, reservedEnvName)
}

// validVariables checks names and sizes; reserved, when set, refuses the names the build sets itself.
func validVariables(what string, values map[string]string, reserved func(string) error) error {
	if len(values) > MaxEnvVars {
		return fmt.Errorf("at most %d %ss", MaxEnvVars, what)
	}
	total := 0
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := values[name]
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("%s %q: a name is letters, digits and '_', not starting with a digit", what, name)
		}
		if reserved != nil {
			if err := reserved(name); err != nil {
				return err
			}
		}
		if len(value) > MaxEnvValueBytes || !utf8.ValidString(value) {
			return fmt.Errorf("%s %s: a value is at most %d bytes of text", what, name, MaxEnvValueBytes)
		}
		total += len(name) + len(value)
	}
	if total > maxEnvTotalBytes {
		return fmt.Errorf("%ss hold at most %d KiB in total", what, maxEnvTotalBytes>>10)
	}
	return nil
}

func reservedEnvName(name string) error {
	if reservedEnv[name] || strings.HasPrefix(name, "KUBERNETES_") {
		return fmt.Errorf("build variable %s is set by the build itself", name)
	}
	return nil
}

// PinnedNodeImages are the offered majors pinned by digest; bump them deliberately.
var PinnedNodeImages = map[string]string{
	"20": "node:20-bookworm@sha256:8f693eaa7e0a8e71560c9a82b55fd54c2ae920a2ba5d2cde28bac7d1c01c9ba5",
	"22": "node:22-bookworm@sha256:0e5f906573693feaa1e21057ebdcfdb5bd5021f050b2dc7c9deceb629c7da2a8",
	"24": "node:24-bookworm@sha256:14078204b6bb268c107ceff6d3c43f45a888105e5c2b18b14c32b3409d93a1b0",
}

// NodeImage is the image a build of that Node major runs in: the pinned one, or the operator's mirror.
func NodeImage(mirror, version string) string {
	if mirror == "" {
		return PinnedNodeImages[version]
	}
	return mirror + ":" + version + "-bookworm"
}

// InputError is a refused setting, worded for the caller.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

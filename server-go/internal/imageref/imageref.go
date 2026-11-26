// Package imageref checks that a container image reference names one
// immutable build, so a push to a registry cannot change what a platform
// component runs (EXC-447).
package imageref

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	releaseTag   = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	trunkTag     = regexp.MustCompile(`^main-[0-9a-f]{7,40}$`)
	branchTag    = regexp.MustCompile(`^release-\d+\.\d+-[0-9a-f]{7,40}$`)
)

const acceptedForms = "name@sha256:<64 hex>, or a tag of the form X.Y.Z, vX.Y.Z, main-<sha> or release-X.Y-<sha>"

// RequirePinned accepts a reference that carries a sha256 digest, or a tag that
// names one build: a release version, a trunk build or a release-branch build.
// Moving tags (latest, main, 1.7, stable) and untagged references are refused:
// each resolves to whatever the registry holds at pull time. The reference is
// checked as given; it is never trimmed, so what is validated is what runs.
func RequirePinned(ref string) error {
	if ref == "" {
		return errors.New("image reference is not set")
	}
	if strings.ContainsAny(ref, " \t\r\n") {
		return errors.New("image reference must not contain whitespace")
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && !sha256Digest.MatchString(digest) {
		return fmt.Errorf("image digest must be sha256:<64 hex>, got %q", digest)
	}
	repository, tag, hasTag := splitTag(name)
	if repository == "" {
		return errors.New("image reference has no name before the tag or digest")
	}
	if repository != strings.ToLower(repository) {
		return fmt.Errorf("image name %q must be lowercase", repository)
	}
	if hasDigest {
		return nil
	}
	if !hasTag || tag == "" {
		return fmt.Errorf("image %q has no tag; accepted: %s", name, acceptedForms)
	}
	if !releaseTag.MatchString(tag) && !trunkTag.MatchString(tag) && !branchTag.MatchString(tag) {
		return fmt.Errorf("image tag %q can move; accepted: %s", tag, acceptedForms)
	}
	return nil
}

// splitTag separates the repository from its tag; a colon before the last "/"
// is a registry port, not a tag.
func splitTag(name string) (repository, tag string, hasTag bool) {
	slash := strings.LastIndex(name, "/") + 1
	head, rest, hasTag := strings.Cut(name[slash:], ":")
	return name[:slash] + head, rest, hasTag
}

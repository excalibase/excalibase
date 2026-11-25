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

var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// RequirePinned accepts a reference that carries a sha256 digest, or an explicit
// tag other than "latest". An empty, untagged or "latest" reference is refused:
// each resolves to whatever the registry holds at pull time.
func RequirePinned(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("image reference is not set")
	}
	if strings.ContainsAny(ref, " \t\r\n") {
		return errors.New("image reference must not contain whitespace")
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest {
		if !sha256Digest.MatchString(digest) {
			return fmt.Errorf("image digest must be sha256:<64 hex>, got %q", digest)
		}
		return nil
	}
	lastSegment := name[strings.LastIndex(name, "/")+1:]
	_, tag, hasTag := strings.Cut(lastSegment, ":")
	switch {
	case !hasTag || tag == "":
		return fmt.Errorf("image %q has no tag; name a release version or a digest", name)
	case strings.EqualFold(tag, "latest"):
		return fmt.Errorf("image %q uses the floating tag latest; name a release version or a digest", name)
	}
	return nil
}

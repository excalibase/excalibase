package appregistry

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pushTag is p<unix seconds>-<sha256 hex>: zot deletes untagged manifests on
// its own, and the tag tells the sweep when an image came and what it is.
var pushTag = regexp.MustCompile(`^p([0-9]{1,12})-([0-9a-f]{64})$`)

// PushTag names an image pushed at a time; every image the platform pushes carries one.
func PushTag(digest string, at time.Time) (string, error) {
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("a push tag names a sha256 digest, not %q", digest)
	}
	return "p" + strconv.FormatInt(at.Unix(), 10) + "-" + strings.TrimPrefix(digest, "sha256:"), nil
}

// ParsePushTag reads a tag PushTag made; any other tag is not the platform's.
func ParsePushTag(tag string) (string, time.Time, bool) {
	match := pushTag.FindStringSubmatch(tag)
	if match == nil {
		return "", time.Time{}, false
	}
	seconds, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return "sha256:" + match[2], time.Unix(seconds, 0), true
}

// SplitRepository reads <project>/<app>, as Repository writes it.
func SplitRepository(repository string) (string, string, bool) {
	project, app, found := strings.Cut(repository, "/")
	if !found || !segment.MatchString(project) || !segment.MatchString(app) {
		return "", "", false
	}
	return project, app, true
}

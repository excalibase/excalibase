package storagesvc

import (
	"path"
	"strings"
)

const (
	// maxObjectKeyLen bounds a user key. S3 and R2 allow 1024 bytes for the
	// whole store key; the per-bucket prefix takes part of that, so the
	// user's share is capped below it.
	maxObjectKeyLen = 900
	// stagingSegment is the first path segment of the namespace uploads land
	// in before they are accepted. It is the platform's, not a caller's: no
	// user key may start with it, so nothing a caller names can collide with
	// an upload in flight or reach one.
	stagingSegment = ".staging"
	// stagingPrefix is that namespace as a key prefix, relative to a bucket.
	stagingPrefix = stagingSegment + "/"
)

// validateObjectKey decides what an object key is. Every entry point that
// takes a key from a caller runs it first, so the catalogue and the object
// store always hold the identical string.
//
// A key that is not already canonical is REFUSED, never rewritten. Silent
// normalisation is what let the two planes disagree: the store held the
// cleaned key, the catalogue the raw one, and a listing of the store then
// found keys no row named — indistinguishable from an abandoned upload, and
// collected as one.
func validateObjectKey(key string) error {
	if key == "" {
		return invalidf("object key is required")
	}
	if len(key) > maxObjectKeyLen {
		return invalidf("object key must be at most %d bytes", maxObjectKeyLen)
	}
	if strings.ContainsRune(key, '\\') {
		return invalidf("object key must not contain a backslash")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return invalidf("object key must not contain control characters")
		}
	}
	if strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return invalidf("object key must not start or end with %q", "/")
	}
	for _, segment := range strings.Split(key, "/") {
		switch segment {
		case "":
			return invalidf("object key must not contain an empty path segment")
		case ".", "..":
			return invalidf("object key must not contain a %q path segment", segment)
		}
	}
	if strings.HasPrefix(key, stagingPrefix) || key == stagingSegment {
		return invalidf("object key must not start with %q", stagingSegment)
	}
	// Belt and braces: after the rules above the key is already canonical, so
	// a difference here means a rule is missing rather than that the key
	// needs cleaning.
	if cleaned := strings.TrimPrefix(path.Clean("/"+key), "/"); cleaned != key {
		return invalidf("object key %q is not canonical", key)
	}
	return nil
}

// validateKeyPrefix checks a listing prefix. A prefix is not a key: it may be
// empty (the whole bucket) and it may end at a "folder" boundary, so it is
// allowed a trailing slash. Everything else a key may not contain, a prefix
// may not either — including the staging namespace, which is the platform's.
func validateKeyPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.HasPrefix(prefix, stagingSegment) {
		return invalidf("prefix must not name %q", stagingSegment)
	}
	return validateObjectKey(strings.TrimSuffix(prefix, "/"))
}

// stagingObjectKey is where an upload's bytes live until they are accepted.
// The id is minted by the platform, so the key is machine-built and never
// carries anything a caller chose.
func stagingObjectKey(uploadID string) string {
	return stagingPrefix + uploadID
}

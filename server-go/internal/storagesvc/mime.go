package storagesvc

import (
	"mime"
	"strings"
)

// normaliseMIME reduces a Content-Type to the value the allow-list is
// compared against: the media type alone, lower-cased, parameters dropped.
//
// Both sides of the comparison go through it, so "IMAGE/PNG" and
// "image/png; charset=binary" are the same type as "image/png" — casing and
// parameters are not a way to look like a different type. Anything that is
// not a well-formed media type is rejected rather than guessed at, so a
// value crafted to confuse a string comparison ("image/png x=", a bare
// "image") never reaches the allow-list.
func normaliseMIME(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", invalidf("contentType is required")
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return "", invalidf("contentType %q is not a valid media type", value)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.Count(mediaType, "/") != 1 {
		return "", invalidf("contentType %q is not a valid media type", value)
	}
	kind, subtype, _ := strings.Cut(mediaType, "/")
	if kind == "" || subtype == "" {
		return "", invalidf("contentType %q is not a valid media type", value)
	}
	return mediaType, nil
}

// checkMIMEAllowlist reports whether a normalised media type is permitted by
// a bucket. An empty allow-list permits any type; "*/*" is an explicit
// wildcard. Allow-list entries are normalised too, so a bucket configured
// with "IMAGE/PNG" behaves the same as one configured with "image/png".
func checkMIMEAllowlist(allowed []string, mediaType string) error {
	if len(allowed) == 0 {
		return nil
	}
	for _, entry := range allowed {
		candidate := strings.ToLower(strings.TrimSpace(entry))
		if candidate == "*/*" {
			return nil
		}
		normalised, err := normaliseMIME(candidate)
		if err != nil {
			// A malformed allow-list entry matches nothing; it must never
			// widen the bucket by accident.
			continue
		}
		if normalised == mediaType {
			return nil
		}
	}
	return invalidf("mime type %q not allowed in bucket", mediaType)
}

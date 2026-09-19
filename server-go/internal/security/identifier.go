package security

import (
	"fmt"
	"regexp"
	"strings"
)

// maxIdentifierLength bounds a caller-supplied identifier. Every identifier
// this validator guards ends up as a filesystem name, and most filesystems
// cap a single component at 255 bytes; 128 leaves room for the extensions
// and suffixes the callers append.
const maxIdentifierLength = 128

// identifierPattern is the conservative charset: the first character must be
// alphanumeric (so no leading dot can form a dot segment or a hidden file),
// and the rest may add dot, hyphen and underscore. Separators, NUL and every
// other byte fall outside it.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateIdentifier rejects a caller-supplied identifier that must not be
// trusted as a storage key — a snapshot id, a parameter-group name. It is the
// handler-boundary check: reject the request outright rather than let a name
// reach a path join and decide what it means there.
//
// SafePathComponent stays the last-line sanitizer at the file-IO boundary;
// this is stricter and runs first, so a rejected request never reaches the
// store at all.
func ValidateIdentifier(value string) error {
	if value == "" {
		return fmt.Errorf("identifier is empty")
	}
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("identifier is longer than %d characters", maxIdentifierLength)
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("identifier contains a dot segment")
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("identifier contains characters outside the allowed set")
	}
	return nil
}

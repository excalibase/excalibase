package tableimport

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/permissions"
)

const maxIdentifierBytes = 63

// Lower-case only: the engine's permission names and GraphQL fields expect
// it, and it never needs quoting by a human writing SQL later.
var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

var nonIdentifier = regexp.MustCompile(`[^a-z0-9_]+`)
var repeatedUnderscore = regexp.MustCompile(`_{2,}`)

// ValidateIdentifier accepts a lower-case Postgres identifier.
func ValidateIdentifier(name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("%q must be lower-case letters, digits and underscores, starting with a letter or underscore, at most 63 characters", name)
	}
	return nil
}

// ValidateTargetSchema refuses schemas the platform or Postgres own.
func ValidateTargetSchema(name string) error {
	if err := ValidateIdentifier(name); err != nil {
		return err
	}
	if !permissions.IsServedSchema(name) {
		return errors.New("tables cannot be imported into a system or platform schema")
	}
	return nil
}

// ColumnNames turns a header row into unique lower-case identifiers. Without
// a header the columns are numbered.
func ColumnNames(header []string, hasHeader bool) []string {
	names := make([]string, len(header))
	taken := make(map[string]bool, len(header))
	for i, raw := range header {
		base := ""
		if hasHeader {
			base = sanitise(raw)
		}
		if base == "" {
			base = "column_" + strconv.Itoa(i+1)
		}
		names[i] = unique(base, taken)
		taken[names[i]] = true
	}
	return names
}

func sanitise(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = nonIdentifier.ReplaceAllString(name, "_")
	name = repeatedUnderscore.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "c_" + name
	}
	return truncate(name, maxIdentifierBytes)
}

func unique(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suffix := "_" + strconv.Itoa(n)
		candidate := truncate(base, maxIdentifierBytes-len(suffix)) + suffix
		if !taken[candidate] {
			return candidate
		}
	}
}

// truncate is byte-safe: sanitised names are ASCII.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

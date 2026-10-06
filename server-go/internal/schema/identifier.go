package schema

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/lib/pq"
)

// MaxIdentifierBytes is Postgres's NAMEDATALEN-1: a longer name is silently cut.
const MaxIdentifierBytes = 63

// InvalidNameError is a name the caller chose that Postgres would not keep as given.
type InvalidNameError struct{ msg string }

func (e *InvalidNameError) Error() string { return e.msg }

// CheckIdentifier refuses an empty name or one Postgres would truncate.
// Any other characters stay allowed: every name is quoted.
func CheckIdentifier(kind, name string) error {
	label := capitalize(kind)
	if name == "" {
		return &InvalidNameError{fmt.Sprintf("%s name is required", label)}
	}
	if len(name) > MaxIdentifierBytes {
		return &InvalidNameError{fmt.Sprintf("%s names can be at most %d characters; this one has %d", label, MaxIdentifierBytes, len(name))}
	}
	return nil
}

// NameRule is the rule a new table, column or schema name follows; Studio
// shows the same sentence.
const NameRule = "Use lowercase letters, numbers and underscores, starting with a letter or underscore; at most 63 characters."

// plainIdentifier is a name that never needs quoting. Permissions, realtime
// and the engine's GraphQL fields accept only these, so a table named any
// other way could never be exposed.
var plainIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// IsPlainIdentifier reports whether name follows NameRule.
func IsPlainIdentifier(name string) bool { return plainIdentifier.MatchString(name) }

// CheckNewName refuses a table, column or schema name that does not follow NameRule.
func CheckNewName(kind, name string) error {
	if err := CheckIdentifier(kind, name); err != nil {
		return err
	}
	if !IsPlainIdentifier(name) {
		return &InvalidNameError{fmt.Sprintf("%s name %q is not allowed. %s", capitalize(kind), name, NameRule)}
	}
	return nil
}

func checkOptionalNewName(kind string, name *string) error {
	if name == nil {
		return nil
	}
	return CheckNewName(kind, *name)
}

// InputError is a request our own validation refused: the caller's mistake.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func invalidInput(format string, args ...any) error {
	return &InputError{fmt.Sprintf(format, args...)}
}

func checkOptionalIdentifier(kind string, name *string) error {
	if name == nil {
		return nil
	}
	return CheckIdentifier(kind, *name)
}

// checkNameLength refuses only a name Postgres would truncate; an empty name
// is left to the caller's own rule (an index may let Postgres choose one).
func checkNameLength(kind, name string) error {
	if name == "" {
		return nil
	}
	return CheckIdentifier(kind, name)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// userMessage is err as a user reads it: a Postgres refusal without the
// driver's "pq: " prefix, keeping any context our code wrapped around it.
func userMessage(err error) string {
	msg := err.Error()
	var pgErr *pq.Error
	if errors.As(err, &pgErr) {
		return strings.Replace(msg, pgErr.Error(), pgErr.Message, 1)
	}
	return msg
}

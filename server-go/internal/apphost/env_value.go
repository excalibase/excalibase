package apphost

import (
	"errors"
	"unicode"
)

// ErrEnvValueControlCharacter refuses a value carrying a control character
// other than tab, newline or carriage return. A NUL in particular cannot be
// stored, and none of them belong in an environment variable.
var ErrEnvValueControlCharacter = errors.New("value must not contain control characters other than tab, newline or carriage return")

// ValidateEnvValue checks an environment value or secret before it is stored.
func ValidateEnvValue(value string) error {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return ErrEnvValueControlCharacter
		}
	}
	return nil
}

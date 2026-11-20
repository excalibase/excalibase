package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Bounds of a password a customer chooses for their database (EXC-759). Sixteen
// characters is what lets a passphrase of a few ordinary words qualify.
const (
	minDatabasePasswordLength   = 16
	maxDatabasePasswordLength   = 128
	minDatabasePasswordDistinct = 8
)

// formulaStarts are the characters a spreadsheet treats as the start of a formula.
const formulaStarts = "=+-@"

// ErrWeakDatabasePassword is a chosen password that does not meet the policy;
// the message says how, and never repeats the password.
var ErrWeakDatabasePassword = errors.New("weak database password")

// ValidateDatabasePassword checks a password the customer chose.
func ValidateDatabasePassword(password string) error {
	runes := []rune(password)
	if len(runes) < minDatabasePasswordLength {
		return fmt.Errorf("%w: use at least %d characters (a passphrase of several words works)", ErrWeakDatabasePassword, minDatabasePasswordLength)
	}
	if len(runes) > maxDatabasePasswordLength {
		return fmt.Errorf("%w: use at most %d characters", ErrWeakDatabasePassword, maxDatabasePasswordLength)
	}
	// A spreadsheet reads these as a formula, so the CSV a customer downloads
	// could not hold the password as typed.
	if strings.ContainsRune(formulaStarts, runes[0]) {
		return fmt.Errorf("%w: it cannot start with one of %s", ErrWeakDatabasePassword, formulaStarts)
	}
	distinct := map[rune]struct{}{}
	for _, r := range runes {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: control characters are not allowed", ErrWeakDatabasePassword)
		}
		distinct[r] = struct{}{}
	}
	if len(distinct) < minDatabasePasswordDistinct {
		return fmt.Errorf("%w: use at least %d different characters", ErrWeakDatabasePassword, minDatabasePasswordDistinct)
	}
	return nil
}

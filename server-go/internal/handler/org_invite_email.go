package handler

import (
	"net/mail"
	"strings"
)

// isEmailAddress reports whether an invite names a single bare address, the
// kind a sign-up can later match; display names and lists are refused.
func isEmailAddress(address string) bool {
	parsed, err := mail.ParseAddress(address)
	return err == nil && parsed.Address == address && strings.Contains(address, ".")
}

// Package logsafe makes request-derived values safe to write to the log.
package logsafe

import "strings"

// sanitizer strips the line terminators an attacker would use to forge
// extra log records.
var sanitizer = strings.NewReplacer("\n", "", "\r", "")

// Value returns s without line terminators.
func Value(s string) string {
	return sanitizer.Replace(s)
}

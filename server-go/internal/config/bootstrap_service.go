package config

import "strings"

// ParseBootstrapServicePermissions reads BOOTSTRAP_SERVICE_PERMISSIONS, the
// comma-separated capability list of the svc-bootstrap token (EXC-485).
// Validation of each entry is the capability parser's job.
func ParseBootstrapServicePermissions(raw string) []string {
	permissions := []string{}
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			permissions = append(permissions, entry)
		}
	}
	return permissions
}

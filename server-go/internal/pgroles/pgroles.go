// Package pgroles is the one list of Postgres role names a project's
// database holds for the platform, and the SQL that lets a project's owner
// create its own roles without reaching any of them.
package pgroles

import (
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// reservedNames are the roles a project database holds that are not the
// owner's: the platform's cert-login roles, CloudNativePG's, the DocumentDB
// gateway, the Mongo users group and the document browser's Mongo login.
var reservedNames = append(slices.Clone(tenantcert.PlatformRoles),
	"postgres", "streaming_replica",
	config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup, config.DocumentDBBrowserLogin,
)

// reservedPrefixes cover Postgres' predefined roles and every role the
// platform, DocumentDB or CloudNativePG creates now or may create later.
var reservedPrefixes = []string{"pg_", "excalibase", "documentdb", "cnpg"}

// ReservedNames returns a copy of the reserved role names.
func ReservedNames() []string { return slices.Clone(reservedNames) }

// ReservedPrefixes returns a copy of the reserved role name prefixes.
func ReservedPrefixes() []string { return slices.Clone(reservedPrefixes) }

// IsReserved reports whether a customer may not create, alter or drop a role
// of this name. Case is ignored, so a look-alike is refused too.
func IsReserved(name string) bool {
	lower := strings.ToLower(name)
	if slices.Contains(reservedNames, lower) {
		return true
	}
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

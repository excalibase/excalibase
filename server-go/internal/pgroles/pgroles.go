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
	"postgres", "streaming_replica", ReaderRole, RuntimeRole,
	config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup, config.DocumentDBBrowserLogin,
)

// reservedPrefixes cover Postgres' predefined roles and every role the
// platform, DocumentDB or CloudNativePG creates now or may create later.
var reservedPrefixes = []string{"pg_", "excalibase", "documentdb", "cnpg"}

// platformSchemaRoles are the exact roles the platform itself creates that may
// own or hold rights in its auth and excalibase_cdc schemas. It is a list of
// names, never prefixes: the project owner can create any role it likes, and
// excalibase_app, excalibase_runtime and excalibase_reader are not here
// because customers hold their passwords. Predefined pg_ roles are the
// server's, which refuses to create one by that prefix.
var platformSchemaRoles = append(slices.DeleteFunc(slices.Clone(tenantcert.PlatformRoles), func(name string) bool { return name == "excalibase_app" }),
	"postgres", "streaming_replica",
	config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup, config.DocumentDBBrowserLogin,
)

// PlatformSchemaRoles returns a copy of the roles the platform's schemas trust.
func PlatformSchemaRoles() []string { return slices.Clone(platformSchemaRoles) }

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

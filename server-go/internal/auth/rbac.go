package auth

type Permission string

const (
	PermProvision       Permission = "provision"
	PermDelete          Permission = "delete"
	PermViewInstances   Permission = "view_instances"
	PermViewCredentials Permission = "view_credentials"
	PermManageBackups   Permission = "manage_backups"
	PermRestore         Permission = "restore"
	PermApplyMigrations Permission = "apply_migrations"
	PermManageSnapshots Permission = "manage_snapshots"
	PermManageSetup     Permission = "manage_setup"
	PermManageUsers     Permission = "manage_users"
	PermManageFunctions Permission = "manage_functions"
	PermViewAny         Permission = "view_any"
)

var rolePermissions = map[string]map[Permission]bool{
	"platform_admin": {
		PermProvision: true, PermDelete: true, PermViewInstances: true,
		PermViewCredentials: true, PermManageBackups: true, PermRestore: true,
		PermApplyMigrations: true, PermManageSnapshots: true, PermManageSetup: true,
		PermManageUsers: true, PermManageFunctions: true, PermViewAny: true,
	},
	"platform_operator": {
		PermProvision: true, PermDelete: true, PermViewInstances: true,
		PermViewCredentials: true, PermManageBackups: true,
		PermApplyMigrations: true, PermManageSnapshots: true,
		PermManageFunctions: true, PermViewAny: true,
	},
	"platform_viewer": {
		PermViewInstances: true, PermViewAny: true,
	},
	// Dashboard users have no platform permissions — they use org/project roles
	"user": {},
}

func HasPermission(role string, perm Permission) bool {
	perms, ok := rolePermissions[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// IsValidPlatformRole reports whether role is a known platform-level role.
// Used to validate user input that names a role — never trust an arbitrary
// string as a role assignment.
func IsValidPlatformRole(role string) bool {
	_, ok := rolePermissions[role]
	return ok
}

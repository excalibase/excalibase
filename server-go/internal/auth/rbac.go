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
	PermViewAny         Permission = "view_any"
)

var rolePermissions = map[string]map[Permission]bool{
	"admin": {
		PermProvision: true, PermDelete: true, PermViewInstances: true,
		PermViewCredentials: true, PermManageBackups: true, PermRestore: true,
		PermApplyMigrations: true, PermManageSnapshots: true, PermManageSetup: true,
		PermManageUsers: true, PermViewAny: true,
	},
	"operator": {
		PermProvision: true, PermDelete: true, PermViewInstances: true,
		PermViewCredentials: true, PermManageBackups: true,
		PermApplyMigrations: true, PermManageSnapshots: true, PermViewAny: true,
	},
	"viewer": {
		PermViewInstances: true, PermViewAny: true,
	},
}

func HasPermission(role string, perm Permission) bool {
	perms, ok := rolePermissions[role]
	if !ok {
		return false
	}
	return perms[perm]
}

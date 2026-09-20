package auth

import "github.com/excalibase/provisioning-poc/internal/domain"

// OrgPermission represents an org-level action.
type OrgPermission string

const (
	OrgPermManageMembers OrgPermission = "manage_members"
	OrgPermCreateProject OrgPermission = "create_project"
	OrgPermDeleteProject OrgPermission = "delete_project"
	OrgPermUpdateOrg     OrgPermission = "update_org"
	OrgPermDeleteOrg     OrgPermission = "delete_org"
	OrgPermViewProjects  OrgPermission = "view_projects"
)

var orgRolePermissions = map[string]map[OrgPermission]bool{
	domain.OrgRoleOwner: {
		OrgPermManageMembers: true, OrgPermCreateProject: true,
		OrgPermDeleteProject: true, OrgPermUpdateOrg: true,
		OrgPermDeleteOrg: true, OrgPermViewProjects: true,
	},
	domain.OrgRoleAdmin: {
		OrgPermManageMembers: true, OrgPermCreateProject: true,
		OrgPermDeleteProject: true, OrgPermUpdateOrg: true,
		OrgPermViewProjects: true,
	},
	domain.OrgRoleDeveloper: {
		OrgPermViewProjects: true,
	},
	domain.OrgRoleViewer: {
		OrgPermViewProjects: true,
	},
}

// HasOrgPermission checks if an org role has the given permission.
func HasOrgPermission(role string, perm OrgPermission) bool {
	perms, ok := orgRolePermissions[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// orgRoleRank ranks org roles for hierarchical comparison — Owner ⊇ Admin ⊇
// Developer ⊇ Viewer, each role holding every capability of the ones below.
// Unknown roles rank 0 (deny). Used to gate the tenant/project plane by role.
func orgRoleRank(role string) int {
	switch role {
	case domain.OrgRoleOwner:
		return 4
	case domain.OrgRoleAdmin:
		return 3
	case domain.OrgRoleDeveloper:
		return 2
	case domain.OrgRoleViewer:
		return 1
	default:
		return 0
	}
}

// OrgRoleAtLeast reports whether `have` is at or above `want` in the org-role
// hierarchy (and is a known role). E.g. an admin satisfies a developer minimum.
func OrgRoleAtLeast(have, want string) bool {
	h := orgRoleRank(have)
	return h > 0 && h >= orgRoleRank(want)
}

// ProjectPermission represents a project-level action.
type ProjectPermission string

const (
	ProjectPermDDL    ProjectPermission = "ddl"    // CREATE/ALTER/DROP tables
	ProjectPermDML    ProjectPermission = "dml"    // INSERT/UPDATE/DELETE
	ProjectPermRead   ProjectPermission = "read"   // SELECT
	ProjectPermManage ProjectPermission = "manage" // manage project members
)

var projectRolePermissions = map[string]map[ProjectPermission]bool{
	domain.ProjectRoleAdmin: {
		ProjectPermDDL: true, ProjectPermDML: true,
		ProjectPermRead: true, ProjectPermManage: true,
	},
	domain.ProjectRoleEditor: {
		ProjectPermDML: true, ProjectPermRead: true,
	},
	domain.ProjectRoleViewer: {
		ProjectPermRead: true,
	},
}

// HasProjectPermission checks if a project role has the given permission.
func HasProjectPermission(role string, perm ProjectPermission) bool {
	perms, ok := projectRolePermissions[role]
	if !ok {
		return false
	}
	return perms[perm]
}

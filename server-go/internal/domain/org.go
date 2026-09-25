package domain

// Org represents an organization that owns projects.
type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Tier      TierType  `json:"tier"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt *FlexTime `json:"createdAt,omitempty"`
	UpdatedAt *FlexTime `json:"updatedAt,omitempty"`
}

// OrgMember represents a user's membership in an organization.
type OrgMember struct {
	OrgID     string    `json:"orgId"`
	UserID    string    `json:"userId"`
	Role      string    `json:"role"` // owner, admin, developer, viewer
	Email     string    `json:"email,omitempty"`
	Username  string    `json:"username,omitempty"`
	CreatedAt *FlexTime `json:"createdAt,omitempty"`
}

// ProjectMember represents a user's membership in a project.
type ProjectMember struct {
	ProjectID string    `json:"projectId"`
	OrgID     string    `json:"orgId"`
	UserID    string    `json:"userId"`
	Role      string    `json:"role"` // admin, editor, viewer
	Email     string    `json:"email,omitempty"`
	Username  string    `json:"username,omitempty"`
	CreatedAt *FlexTime `json:"createdAt,omitempty"`
}

// PendingInvite represents an invitation for a user who hasn't registered yet.
type PendingInvite struct {
	ID        int64     `json:"id"`
	OrgID     string    `json:"orgId"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invitedBy"`
	TokenHash string    `json:"-"`
	ExpiresAt *FlexTime `json:"expiresAt,omitempty"`
	CreatedAt *FlexTime `json:"createdAt,omitempty"`
}

// Org role constants
const (
	OrgRoleOwner     = "owner"
	OrgRoleAdmin     = "admin"
	OrgRoleDeveloper = "developer"
	OrgRoleViewer    = "viewer"
)

// Project role constants
const (
	ProjectRoleAdmin  = "admin"
	ProjectRoleEditor = "editor"
	ProjectRoleViewer = "viewer"
)

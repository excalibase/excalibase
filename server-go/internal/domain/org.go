package domain

import (
	"strings"
	"unicode/utf8"
)

// MaxOrgNameLength bounds an organization's display name, in characters.
const MaxOrgNameLength = 64

// NormalizeOrgName trims the name and reports whether it is 1 to
// MaxOrgNameLength characters long.
func NormalizeOrgName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	length := utf8.RuneCountInString(trimmed)
	if length == 0 || length > MaxOrgNameLength {
		return "", false
	}
	return trimmed, true
}

// Org represents an organization that owns projects.
type Org struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Slug string   `json:"slug"`
	Tier TierType `json:"tier"`
	// OwnerID is the account that created the org. It never changes, so the
	// free allowance stays with the creator whoever owns the org later.
	OwnerID   string    `json:"ownerId"`
	// The caller's membership role, set only by the orgs-for-user listing.
	Role      string    `json:"role,omitempty"`
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

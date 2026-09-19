package domain

import "time"

// User kinds. A human signs in with a password and may join organizations; a
// service principal exists only to own capability tokens and can do neither.
const (
	UserKindHuman   = "human"
	UserKindService = "service"
)

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin, operator, viewer
	Active       bool   `json:"active"`
	// Kind is UserKindHuman or UserKindService. Empty rows predate the column
	// and are read as human.
	Kind      string     `json:"kind,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// IsService reports whether the user is a service principal.
func (u *User) IsService() bool {
	return u != nil && u.Kind == UserKindService
}

type AccessToken struct {
	TokenHash   string `json:"-"`
	TokenPrefix string `json:"tokenPrefix"`
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	// Scopes is a comma-separated list of capability tags ("session",
	// "read", "admin"). Empty = legacy all-purpose token (predates this
	// column). New tokens always carry at least one scope.
	Scopes string `json:"scopes,omitempty"`
	// ProjectID binds the token to one project: every project-scoped route
	// whose path names a different project answers 404. Empty = the token
	// reaches every project its user is a member of.
	ProjectID string `json:"projectId,omitempty"`
	// Permissions is the capability list (<resource>:<action>[:<selector>]).
	// Non-empty makes this a capability token: it reaches only the handlers
	// that declare a matching capability, whatever the owner's role allows.
	// Empty = an ordinary human PAT or session.
	Permissions []string   `json:"permissions,omitempty"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsed    *time.Time `json:"lastUsed,omitempty"`
}

type AuditEntry struct {
	ID         int64      `json:"id"`
	UserID     string     `json:"userId,omitempty"`
	Action     string     `json:"action"`
	Resource   string     `json:"resource"`
	ResourceID string     `json:"resourceId,omitempty"`
	Details    string     `json:"details,omitempty"`
	IPAddress  string     `json:"ipAddress,omitempty"`
	Timestamp  *time.Time `json:"timestamp"`
}

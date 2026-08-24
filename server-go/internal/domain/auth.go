package domain

import "time"

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"` // admin, operator, viewer
	Active       bool       `json:"active"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`
}

type AccessToken struct {
	TokenHash   string `json:"-"`
	TokenPrefix string `json:"tokenPrefix"`
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	// Scopes is a comma-separated list of capability tags ("session",
	// "read", "admin"). Empty = legacy all-purpose token (predates this
	// column). New tokens always carry at least one scope.
	Scopes    string     `json:"scopes,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
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

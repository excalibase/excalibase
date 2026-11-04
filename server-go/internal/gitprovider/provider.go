// Package gitprovider is what git builds of sites need from a git host
// (EXC-354): who may connect which repository, the head of a branch, a
// short-lived clone credential, and the webhook a push arrives on. GitHub is
// the first host; GitLab implements the same interface later.
package gitprovider

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	// ErrBadSignature is a webhook whose signature does not match the shared secret.
	ErrBadSignature = errors.New("webhook signature does not match")
	// ErrNotFound is a repository, branch or installation the host does not show us.
	ErrNotFound = errors.New("not found on the git host")
	// ErrAuthorization is a sign-in code the host refused.
	ErrAuthorization = errors.New("the git host refused the sign-in")
)

// Repository is one repository a provider can clone.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"fullName"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
}

// Installation is the provider-side grant that lets the platform read repositories.
type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

// Access is one installation and the repositories in it a signed-in user may read.
type Access struct {
	Installation Installation `json:"installation"`
	Repositories []Repository `json:"repositories"`
}

// Commit is a branch head.
type Commit struct {
	SHA     string
	Message string
}

// CloneCredential reads one repository's contents until it expires.
type CloneCredential struct {
	Token     string
	ExpiresAt time.Time
}

// Push is a push to a branch.
type Push struct {
	InstallationID int64
	RepositoryID   int64
	Repository     string
	Branch         string
	Commit         Commit
	// Deleted is a push that removed the branch.
	Deleted bool
}

// Event is one webhook delivery: a push, or repositories leaving the platform's reach.
type Event struct {
	DeliveryID string
	Push       *Push
	// RemovedInstallation is an installation that was deleted or suspended.
	RemovedInstallation int64
	// RemovedRepositories are repositories an installation no longer covers.
	RemovedRepositories []int64
	RemovedFrom         int64
}

// Provider is one git host.
type Provider interface {
	Name() string
	// AuthorizeURL sends a user to sign in, coming back to redirectURL with state.
	AuthorizeURL(state, redirectURL string) string
	// InstallURL sends a user to install the platform's app on their account or organisation.
	InstallURL(state string) string
	// UserAccess exchanges a sign-in code for what the user may connect; the user's token is not kept.
	UserAccess(ctx context.Context, code, redirectURL string) ([]Access, error)
	Repository(ctx context.Context, installationID, repositoryID int64) (Repository, error)
	BranchHead(ctx context.Context, installationID int64, repository Repository, branch string) (Commit, error)
	// CloneCredential reads one repository's contents and nothing else.
	CloneCredential(ctx context.Context, installationID, repositoryID int64) (CloneCredential, error)
	RevokeCloneCredential(ctx context.Context, token string) error
	CloneURL(repository Repository) string
	// ParseWebhook verifies a delivery's signature before reading it.
	ParseWebhook(header http.Header, body []byte) (Event, error)
}

// FileChecker answers whether a regular file is at a path in a commit; a missing
// file is false, and anything the host could not answer is an error.
type FileChecker interface {
	FileExists(ctx context.Context, installationID int64, repository Repository, commit, path string) (bool, error)
}

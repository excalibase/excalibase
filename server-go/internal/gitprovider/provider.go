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

// Pull request actions a preview acts on (EXC-660).
const (
	PullRequestOpened      = "opened"
	PullRequestReopened    = "reopened"
	PullRequestSynchronize = "synchronize"
	PullRequestClosed      = "closed"
)

// PullRequest is a pull request opened, pushed to, reopened or closed.
type PullRequest struct {
	Action         string
	InstallationID int64
	RepositoryID   int64
	Repository     string
	Number         int
	// Commit is the head; its message is the pull request's title.
	Commit     Commit
	HeadBranch string
	// FromFork is a head in another repository, or one whose repository is gone.
	FromFork bool
}

// PullRequestState is a pull request as the host reads it now.
type PullRequestState struct {
	Open     bool
	HeadSHA  string
	FromFork bool
}

// Commit status states.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"
)

// CommitStatus is one status posted on a commit; TargetURL may be empty.
type CommitStatus struct {
	State       string
	TargetURL   string
	Description string
	Context     string
}

// Event is one webhook delivery: a push, a pull request, or repositories leaving the platform's reach.
type Event struct {
	DeliveryID  string
	Push        *Push
	PullRequest *PullRequest
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
	// PullRequest reads a pull request's state now, so a replayed event is not trusted.
	PullRequest(ctx context.Context, installationID int64, repository Repository, number int) (PullRequestState, error)
	// SetCommitStatus posts a status on a commit.
	SetCommitStatus(ctx context.Context, installationID int64, repository Repository, sha string, status CommitStatus) error
}

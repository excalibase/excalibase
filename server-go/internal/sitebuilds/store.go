package sitebuilds

import (
	"context"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// ErrLeaseLost is a build another replica declared lost and finished.
var ErrLeaseLost = errors.New("the build's lease lapsed and it was finished elsewhere")

// SignIn is a git-host sign-in in flight, bound to the user and site it started from.
type SignIn struct {
	UserID    string
	ProjectID string
	SiteID    string
	// BrowserHash is the hashed key the starting browser holds.
	BrowserHash string
}

// Store persists connections, the build queue of sites and apps, sign-ins and grants. Every
// site- or app-scoped method takes the project id, so one project never reaches another's.
type Store interface {
	// GetConnection answers nil, no error, for a site with none.
	GetConnection(ctx context.Context, projectID, siteID string) (*Connection, error)
	// SaveConnection creates or replaces it; ErrSiteNotFound when the project holds no such site.
	SaveConnection(ctx context.Context, connection *Connection) error
	DeleteConnection(ctx context.Context, projectID, siteID string) error
	ConnectionsForPush(ctx context.Context, provider string, installationID, repositoryID int64, branch string) ([]*Connection, error)
	// ForgetInstallation drops connections through an installation, or only those to the given repositories.
	ForgetInstallation(ctx context.Context, provider string, installationID int64, repositoryIDs []int64) (int, error)

	// GetAppConnection answers nil, no error, for an app with none.
	GetAppConnection(ctx context.Context, projectID, appID string) (*AppConnection, error)
	// SaveAppConnection creates or replaces it; apphost.ErrAppNotFound when the project holds no such app.
	SaveAppConnection(ctx context.Context, connection *AppConnection) error
	DeleteAppConnection(ctx context.Context, projectID, appID string) error
	AppConnectionsForPush(ctx context.Context, provider string, installationID, repositoryID int64, branch string) ([]*AppConnection, error)

	// CancelBuilds stops the target's queued and running builds.
	CancelBuilds(ctx context.Context, target Target, reason string) error
	DeactivateConnection(ctx context.Context, target Target, reason string) error
	// RenameRepository records a repository's current owner/name, read by its id.
	RenameRepository(ctx context.Context, target Target, repositoryID int64, fullName string) error

	SaveSignIn(ctx context.Context, stateHash string, signIn SignIn, expires time.Time) error
	// TakeSignIn answers the sign-in once, while unexpired; ErrStateInvalid otherwise.
	TakeSignIn(ctx context.Context, stateHash string, now time.Time) (SignIn, error)
	// DeliverySeen reports a webhook delivery handled in the last days; RecordDelivery notes one.
	DeliverySeen(ctx context.Context, provider, deliveryID string) (bool, error)
	RecordDelivery(ctx context.Context, provider, deliveryID string, now time.Time) error
	// DeliveryQueued reports a build of the target already made from the delivery.
	DeliveryQueued(ctx context.Context, target Target, deliveryID string) (bool, error)
	// RequeueBuild hands a build this holder is running back to the queue, or cancels it when a newer one waits.
	RequeueBuild(ctx context.Context, buildID, holder string) error
	ReplaceGrants(ctx context.Context, userID, provider string, access []gitprovider.Access, verifiedAt time.Time) error
	// Grants answers what the user's sign-in since then showed them.
	Grants(ctx context.Context, userID, provider string, since time.Time) ([]gitprovider.Access, error)

	// Enqueue cancels the target's queued builds as superseded and queues this one,
	// or records it as it is when it was refused before queueing (FAILED).
	// A delivery already recorded for the target answers false.
	Enqueue(ctx context.Context, build *Build, keep int) (bool, error)
	// ClaimNext takes the oldest queued build of the given kinds that fits the slots (running builds
	// of every kind count), its project's cap for that kind, and one running build per site or app.
	ClaimNext(ctx context.Context, slots int, holder string, leaseUntil time.Time, kinds []string) (*Build, error)
	ExtendLease(ctx context.Context, buildID, holder string, until time.Time) error
	SaveLog(ctx context.Context, buildID, holder, log string) error
	FinishBuild(ctx context.Context, build *Build, holder string) error
	// TakeAppBuildSlot counts one app build against the project's builds in the hour before now,
	// answering how long to wait when perHour are already counted.
	TakeAppBuildSlot(ctx context.Context, projectID string, now time.Time, perHour int) (time.Duration, error)
	// CancelQueued cancels every queued build of the kind, answering how many.
	CancelQueued(ctx context.Context, kind, reason string) (int, error)
	// ExpireLostBuilds fails running builds whose lease lapsed.
	ExpireLostBuilds(ctx context.Context, now time.Time, reason string) ([]LostBuild, error)
	ListBuilds(ctx context.Context, target Target, limit int) ([]*Build, error)
	// GetBuild answers nil, no error, for a build the target does not hold.
	GetBuild(ctx context.Context, target Target, id string) (*Build, error)

	// VisibleSecrets is the one project-wide list builds of both kinds read.
	VisibleSecrets(ctx context.Context, projectID string) ([]string, error)
	SetVisibleSecrets(ctx context.Context, projectID string, names []string, actor string) error
}

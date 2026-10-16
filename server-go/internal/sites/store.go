package sites

import (
	"context"
	"time"
)

// Store persists sites and their deploys. Every method is scoped by project
// id, so a caller holding one project's id never reaches another's site.
type Store interface {
	// Create refuses with ErrSiteNameTaken when any project uses the name
	// (it is a hostname) and ErrSiteLimitReached past maxSites.
	Create(ctx context.Context, site *Site, maxSites int) error
	// Get answers nil, no error, when the project holds no such site.
	Get(ctx context.Context, projectID, id string) (*Site, error)
	List(ctx context.Context, projectID string) ([]*Site, error)
	UpdateSettings(ctx context.Context, projectID, id string, spaFallback bool) (*Site, error)
	// Delete removes the site and its deploy history.
	Delete(ctx context.Context, projectID, id string) error
	// ClaimDeploy takes the site's lease until the given time, refusing with
	// ErrSiteBusy while another unexpired lease holds it.
	ClaimDeploy(ctx context.Context, projectID, id string, until time.Time) error
	// ReleaseDeploy drops the lease; a site already deleted is not an error.
	ReleaseDeploy(ctx context.Context, projectID, id string) error
	// MarkCorsClaimed records that the site's origin was offered to the project's CORS list.
	MarkCorsClaimed(ctx context.Context, projectID, id string) error
	CreateDeploy(ctx context.Context, deploy *Deploy) error
	// FinishDeploy records the outcome; a succeeded deploy becomes the site's live one in the same write.
	FinishDeploy(ctx context.Context, deploy *Deploy) error
	// SetLive points the site at an earlier deploy.
	SetLive(ctx context.Context, projectID, siteID, deployID string) error
	// GetDeploy answers nil, no error, when the site holds no such deploy.
	GetDeploy(ctx context.Context, projectID, siteID, id string) (*Deploy, error)
	// ListDeploys answers the newest first, at most limit.
	ListDeploys(ctx context.Context, projectID, siteID string, limit int) ([]*Deploy, error)
}

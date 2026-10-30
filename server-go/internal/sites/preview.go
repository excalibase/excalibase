package sites

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Preview sources (EXC-660): a pull request by number, or a branch other than the production one.
const (
	PreviewSourcePullRequest = "pull-request"
	PreviewSourceBranch      = "branch"
)

// Preview statuses. REMOVING is a removal that has not finished on Cloudflare; the sweep retries it.
const (
	PreviewBuilding = "BUILDING"
	PreviewReady    = "READY"
	PreviewFailed   = "FAILED"
	PreviewRemoving = "REMOVING"
)

const (
	// PreviewLifetime is how long a preview lives without a push or a deploy.
	PreviewLifetime = 14 * 24 * time.Hour
	// DefaultMaxPreviews is the install-wide cap when none is configured: each live preview is one Worker script.
	DefaultMaxPreviews = 200
	previewRandomBytes = 16
	previewPrefixMax   = 30
	maxPreviewRef      = 255
	previewSweepBatch  = 50
)

var (
	ErrPreviewsUnavailable = errors.New("previews are not available on this install")
	ErrPreviewNotFound     = errors.New("preview not found")
	ErrPreviewLimitReached = errors.New("the project already has as many live previews as its plan allows; close a pull request or remove a preview")
	ErrPreviewInstallFull  = errors.New("this install has no room for another preview right now; try again later or ask its operator")
	ErrPreviewRemoving     = errors.New("this preview is being removed; push again once it is gone")
)

// previewEncoding is lowercase RFC 4648 base32 without padding: DNS-safe, 26 characters for 16 bytes.
var previewEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Preview is one branch or pull request of a site served at its own unguessable hostname.
type Preview struct {
	ID        string `json:"id"`
	SiteID    string `json:"siteId"`
	ProjectID string `json:"projectId"`
	Source    string `json:"source"`
	// Ref is the branch name, or the pull request number in decimal.
	Ref      string `json:"ref"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
	// CommitSHA is the commit last asked for; DeployedSHA the one now served.
	CommitSHA     string     `json:"commitSha"`
	DeployedSHA   string     `json:"deployedSha,omitempty"`
	FailureReason string     `json:"failureReason,omitempty"`
	FileCount     int        `json:"fileCount"`
	TotalBytes    int64      `json:"totalBytes"`
	CorsClaimed   bool       `json:"-"`
	CreatedBy     string     `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	DeployedAt    *time.Time `json:"deployedAt,omitempty"`
}

// URL is where the preview is served.
func (p *Preview) URL() string { return "https://" + p.Hostname }

// PreviewScriptName is the user Worker serving a preview; the router accepts it as a site script.
func PreviewScriptName(preview *Preview) string { return "site-pv-" + preview.ID }

// previewCorsOwner keys the preview's CORS origin, so removing it releases exactly that origin.
func previewCorsOwner(preview *Preview) string { return "preview-" + preview.ID }

// PreviewStore keeps previews. Every method is scoped by project; a hostname is unique across the install.
type PreviewStore interface {
	// OpenPreview answers the site's preview for (source, ref), marking it BUILDING at commit; it creates
	// preview as given when none exists, refusing with ErrPreviewLimitReached past maxPerProject live
	// previews in the project and ErrPreviewInstallFull past maxInstall across the install, and with
	// ErrPreviewRemoving while the held one is being removed.
	OpenPreview(ctx context.Context, preview *Preview, maxPerProject, maxInstall int) (*Preview, error)
	// GetPreview answers nil, no error, when the site holds no such preview.
	GetPreview(ctx context.Context, projectID, siteID, id string) (*Preview, error)
	FindPreview(ctx context.Context, projectID, siteID, source, ref string) (*Preview, error)
	ListPreviews(ctx context.Context, projectID, siteID string) ([]*Preview, error)
	CountPreviews(ctx context.Context, projectID string) (int, error)
	// ClaimPreview takes the preview's lease, refusing with ErrSiteBusy while another holds it and
	// ErrPreviewNotFound once it is gone or being removed (unless removing is set).
	ClaimPreview(ctx context.Context, projectID, id, lease string, until time.Time, removing bool) error
	ReleasePreview(ctx context.Context, projectID, id, lease string) error
	// FinishPreview records a deploy's outcome while lease holds the preview.
	FinishPreview(ctx context.Context, preview *Preview, lease string) error
	MarkPreviewRemoving(ctx context.Context, projectID, id string) error
	DeletePreview(ctx context.Context, projectID, id, lease string) error
	// DuePreviews answers previews being removed and those not updated since idleBefore.
	DuePreviews(ctx context.Context, idleBefore time.Time, limit int) ([]*Preview, error)
}

// PreviewRequest opens a site's preview for a pull request or branch.
type PreviewRequest struct {
	ProjectID string
	SiteID    string
	Source    string
	Ref       string
	CommitSHA string
	Actor     string
}

func (r PreviewRequest) validate() error {
	switch r.Source {
	case PreviewSourcePullRequest:
		if number, err := strconv.Atoi(r.Ref); err != nil || number <= 0 || strconv.Itoa(number) != r.Ref {
			return &InputError{Reason: "a pull request preview needs the pull request's number"}
		}
	case PreviewSourceBranch:
		if r.Ref == "" || len(r.Ref) > maxPreviewRef || strings.ContainsFunc(r.Ref, func(c rune) bool { return c < 0x21 || c == 0x7f }) {
			return &InputError{Reason: "a branch preview needs a branch name"}
		}
	default:
		return &InputError{Reason: "a preview is of a pull request or a branch"}
	}
	if len(r.CommitSHA) > maxPreviewRef {
		return &InputError{Reason: "that is not a commit"}
	}
	return nil
}

// PreviewsAvailable reports whether this install publishes previews.
func (s *Service) PreviewsAvailable() bool { return s.Available() && s.options.Previews != nil }

func (s *Service) previewStore() (PreviewStore, error) {
	if !s.PreviewsAvailable() {
		return nil, ErrPreviewsUnavailable
	}
	return s.options.Previews, nil
}

func (s *Service) maxInstallPreviews() int {
	if s.options.MaxPreviews > 0 {
		return s.options.MaxPreviews
	}
	return DefaultMaxPreviews
}

// previewHostname is <site name, at most 30>--<128 random bits>: a site name can never hold "--", so it never collides.
func (s *Service) previewHostname(site *Site) (string, error) {
	random := make([]byte, previewRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("make a preview hostname: %w", err)
	}
	prefix := site.Name
	if len(prefix) > previewPrefixMax {
		prefix = strings.TrimRight(prefix[:previewPrefixMax], "-")
	}
	return prefix + "--" + previewEncoding.EncodeToString(random) + "." + s.options.BaseDomain, nil
}

// OpenPreview answers the site's preview for a pull request or branch, creating it within the caps.
func (s *Service) OpenPreview(ctx context.Context, request PreviewRequest) (*Preview, error) {
	store, err := s.previewStore()
	if err != nil {
		return nil, err
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	site, err := s.mustGet(ctx, request.ProjectID, request.SiteID)
	if err != nil {
		return nil, err
	}
	limits, err := s.limitsFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	hostname, err := s.previewHostname(site)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	candidate := &Preview{ID: newPreviewID(), SiteID: site.ID, ProjectID: site.ProjectID, Source: request.Source, Ref: request.Ref,
		Hostname: hostname, Status: PreviewBuilding, CommitSHA: request.CommitSHA, CreatedBy: request.Actor, CreatedAt: now, UpdatedAt: now}
	return store.OpenPreview(ctx, candidate, limits.MaxPreviews, s.maxInstallPreviews())
}

func (s *Service) GetPreview(ctx context.Context, projectID, siteID, id string) (*Preview, error) {
	store, err := s.previewStore()
	if err != nil {
		return nil, err
	}
	return store.GetPreview(ctx, projectID, siteID, id)
}

func (s *Service) ListPreviews(ctx context.Context, projectID, siteID string) ([]*Preview, error) {
	store, err := s.previewStore()
	if err != nil {
		return nil, err
	}
	if _, err := s.mustGet(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return store.ListPreviews(ctx, projectID, siteID)
}

// PreviewUsage answers the project's live previews and its plan's cap.
func (s *Service) PreviewUsage(ctx context.Context, projectID string) (int, int, error) {
	store, err := s.previewStore()
	if err != nil {
		return 0, 0, err
	}
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	used, err := store.CountPreviews(ctx, projectID)
	return used, limits.MaxPreviews, err
}

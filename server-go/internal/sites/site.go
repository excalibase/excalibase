// Package sites is static frontend hosting (EXC-353): a project's built
// frontend, uploaded as an archive, published as a user Worker with static
// assets in the platform's Workers for Platforms namespace. Each deploy is
// kept, so any earlier one can go live again.
package sites

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

var (
	ErrSiteNotFound     = errors.New("site not found")
	ErrSiteNameTaken    = errors.New("that site name is already taken")
	ErrSiteLimitReached = errors.New("the project already holds as many sites as it may")
	ErrSiteBusy         = errors.New("another deploy or deletion of this site is running; try again when it finishes")
	// ErrLeaseLost is a deploy or deletion that outlived its lease, so another one now owns the site.
	ErrLeaseLost      = errors.New("the deploy took longer than allowed and another change now owns the site; deploy again")
	ErrDeployNotFound = errors.New("deploy not found")
	// ErrDeployNotServable is a rollback to a deploy that never went live.
	ErrDeployNotServable = errors.New("only a deploy that succeeded can go live again")
	// ErrRollbackNeedsFiles is Cloudflare asking for files again; their bodies are not kept here.
	ErrRollbackNeedsFiles = errors.New("the CDN no longer holds that deploy's files; deploy its build again instead")
	// ErrSitesUnavailable is this install having no Cloudflare account configured.
	ErrSitesUnavailable = errors.New("sites are not available on this install; run the frontend as a container instead")
)

// Deploy statuses. A deploy is written PENDING before anything is published.
const (
	DeployPending   = "PENDING"
	DeploySucceeded = "SUCCEEDED"
	DeployFailed    = "FAILED"
)

// Site is one hostname serving one project's frontend.
type Site struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	// SPAFallback answers unknown paths with /index.html instead of the nearest 404.html.
	SPAFallback  bool      `json:"spaFallback"`
	LiveDeployID string    `json:"liveDeployId,omitempty"`
	CorsClaimed  bool      `json:"-"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Deploy is one immutable upload of a site's build output.
type Deploy struct {
	ID         string                             `json:"id"`
	SiteID     string                             `json:"siteId"`
	ProjectID  string                             `json:"projectId"`
	Status     string                             `json:"status"`
	Manifest   map[string]cfworkers.ManifestEntry `json:"-"`
	FileCount  int                                `json:"fileCount"`
	TotalBytes int64                              `json:"totalBytes"`
	// SPAFallback is the routing this deploy was published with; a rollback brings it back.
	SPAFallback   bool   `json:"spaFallback"`
	Label         string `json:"label,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	CreatedBy     string `json:"createdBy"`
	// CreatedByName is the deployer's username, read with the history; empty once the account is gone.
	CreatedByName string     `json:"createdByName,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// reservedNames are hostnames a site could pass off as the platform's own.
var reservedNames = map[string]bool{
	"www": true, "api": true, "admin": true, "studio": true, "mail": true, "status": true, "docs": true,
	"dashboard": true, "console": true, "support": true, "billing": true, "login": true, "ns": true,
	"ns1": true, "ns2": true, "mx": true, "smtp": true, "imap": true, "pop": true, "webmail": true,
	"autodiscover": true, "autoconfig": true, "static": true, "assets": true, "cdn": true, "sso": true,
	"oauth": true, "auth": true, "id": true, "account": true, "accounts": true, "security": true, "help": true,
	"blog": true, "graphql": true, "rest": true, "realtime": true, "storage": true, "functions": true,
	"edge": true, "gateway": true, "internal": true, "staging": true, "dev": true, "beta": true, "app": true,
	"apps": true,
}

// ValidateName holds a site name to one DNS label, since it is the hostname's first label.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("a site name is 3 to 63 lowercase letters, digits or hyphens, starting and ending with a letter or digit")
	}
	if strings.Contains(name, "--") {
		return errors.New("a site name may not contain two hyphens in a row")
	}
	if reservedNames[name] || strings.Contains(name, "excalibase") {
		return fmt.Errorf("%q is reserved; choose another site name", name)
	}
	return nil
}

const maxLabelLength = 200

// ValidateLabel bounds the free-text commit or ref a deploy is marked with.
func ValidateLabel(label string) error {
	if len(label) > maxLabelLength {
		return fmt.Errorf("a deploy label is at most %d characters", maxLabelLength)
	}
	if strings.ContainsFunc(label, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("a deploy label may not contain control characters")
	}
	return nil
}

// ScriptName is the user Worker that serves the site.
func ScriptName(site *Site) string { return "site-" + site.ID }

// CorsOwner is the key the project's CORS list records the site's own origin under.
func CorsOwner(site *Site) string { return "site-" + site.ID }

// Origin is the browser origin the site is served from.
func Origin(site *Site) string { return "https://" + site.Hostname }

func assetConfig(spaFallback bool) cfworkers.AssetConfig {
	notFound := cfworkers.NotFound404Page
	if spaFallback {
		notFound = cfworkers.NotFoundSinglePage
	}
	return cfworkers.AssetConfig{HTMLHandling: cfworkers.HTMLAutoTrailingSlash, NotFoundHandling: notFound}
}

// InputError is a refused name, label or setting, worded for the caller.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

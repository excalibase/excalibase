package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrInvalidSiteURL marks a site URL the auth service must not be handed as
// a redirect target.
var ErrInvalidSiteURL = errors.New("invalid site url")

// MaxSiteURLLength caps the stored site URL. A legitimate app URL is short;
// anything longer is a sign of a mistaken paste, not a real deploy target.
const MaxSiteURLLength = 512

// ProjectAuthSettings is a project's per-tenant auth behavior, served on
// GET /api/projects/{id}/info so the auth service can enforce email
// verification and build the correct redirect/callback URLs (EXC-367).
type ProjectAuthSettings struct {
	RequireEmailVerification bool   `json:"requireEmailVerification"`
	SiteURL                  string `json:"siteUrl"`
}

// ValidateSiteURL validates and canonicalises a project's site URL: an
// absolute http/https URL with a host, no userinfo/query/fragment, and no
// trailing slash. A path is allowed. Scheme and host are lower-cased; the
// path is left as given. An empty (or whitespace-only) input is accepted as
// "unset" and returns "".
func ValidateSiteURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > MaxSiteURLLength {
		return "", fmt.Errorf("%w: %q exceeds %d characters", ErrInvalidSiteURL, truncateForError(trimmed), MaxSiteURLLength)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", siteURLError(trimmed)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", siteURLError(trimmed)
	}
	if parsed.Host == "" || parsed.Opaque != "" {
		return "", siteURLError(trimmed)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", siteURLError(trimmed)
	}
	if strings.HasSuffix(parsed.Path, "/") {
		return "", siteURLError(trimmed)
	}

	return scheme + "://" + strings.ToLower(parsed.Host) + parsed.Path, nil
}

// truncateForError keeps an oversized input out of the error message body
// while still giving the caller something to recognise.
func truncateForError(s string) string {
	const maxErrorSnippet = 64
	if len(s) <= maxErrorSnippet {
		return s
	}
	return s[:maxErrorSnippet] + "..."
}

func siteURLError(entry string) error {
	return fmt.Errorf("%w: %q must be an absolute http(s) URL with a host, no userinfo/query/fragment, and no trailing slash", ErrInvalidSiteURL, entry)
}

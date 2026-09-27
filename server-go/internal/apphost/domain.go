package apphost

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
	"k8s.io/apimachinery/pkg/util/validation"
)

// A custom domain moves pending -> issuing once its CNAME is seen pointing at
// the app's own hostname, then active once its certificate is issued.
// Issuing, active and issue_failed are routed and hold the hostname against
// every other app; pending and detached hold nothing.
const (
	DomainPending     = "pending"
	DomainIssuing     = "issuing"
	DomainActive      = "active"
	DomainIssueFailed = "issue_failed"
	DomainDetached    = "detached"
)

const (
	MaxDomainsPerApp = 5
	// DomainRecheckInterval is how often a routed domain's CNAME is checked again.
	DomainRecheckInterval = 24 * time.Hour
	// DomainDetachAfter consecutive failed re-checks take the domain's route away.
	DomainDetachAfter = 3
)

var (
	ErrInvalidDomain = errors.New("invalid custom domain")
	ErrDomainLimit   = fmt.Errorf("an app may have at most %d custom domains", MaxDomainsPerApp)
	ErrDomainExists  = errors.New("the app already has this domain")
	// ErrDomainClaimed refuses a domain another app has already verified.
	ErrDomainClaimed  = errors.New("another app already serves this domain")
	ErrDomainNotFound = errors.New("domain not found")
)

type Domain struct {
	ID                  string     `json:"id"`
	ProjectID           string     `json:"projectId"`
	AppID               string     `json:"appId"`
	Hostname            string     `json:"hostname"`
	Status              string     `json:"status"`
	FailureReason       string     `json:"failureReason,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	VerifiedAt          *time.Time `json:"verifiedAt,omitempty"`
	LastCheckedAt       *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}

// DomainRoutable reports whether the platform routes the domain to the app.
func DomainRoutable(status string) bool {
	return status == DomainIssuing || status == DomainActive || status == DomainIssueFailed
}

// NormalizeCustomDomain refuses what cannot be proven with a CNAME: an apex
// (a CNAME cannot sit there), a public suffix, a wildcard, an address, and
// anything under the platform's own app domain.
func NormalizeCustomDomain(raw, platformDomain string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if problems := validation.IsDNS1123Subdomain(host); len(problems) > 0 || host == "" {
		return "", fmt.Errorf("%w: %q is not a host name", ErrInvalidDomain, raw)
	}
	for _, label := range strings.Split(host, ".") {
		if len(validation.IsDNS1123Label(label)) > 0 {
			return "", fmt.Errorf("%w: %q is not a host name", ErrInvalidDomain, raw)
		}
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("%w: an address is not a domain", ErrInvalidDomain)
	}
	registered, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || registered == host {
		return "", fmt.Errorf("%w: %q is a registered domain or a public suffix; use a subdomain such as www.%s", ErrInvalidDomain, host, host)
	}
	platform := strings.ToLower(platformDomain)
	if platform != "" && (host == platform || strings.HasSuffix(host, "."+platform)) {
		return "", fmt.Errorf("%w: %q is under the platform's own domain", ErrInvalidDomain, host)
	}
	return host, nil
}

// DomainStore persists custom domains, always scoped by project and app.
type DomainStore interface {
	// Add refuses more than MaxDomainsPerApp and a hostname the app already has.
	Add(domain *Domain) error
	List(projectID, appID string) ([]*Domain, error)
	Get(projectID, appID, id string) (*Domain, error)
	// ListRoutable returns the routed domains of every app, for the re-check sweep.
	ListRoutable() ([]*Domain, error)
	// Verify marks the domain issuing, refused with ErrDomainClaimed while another app routes the hostname.
	Verify(projectID, appID, id string, at time.Time) error
	// SetStatus records an observed status; failures counts consecutive failed re-checks.
	SetStatus(id, status, failureReason string, failures int, checkedAt time.Time) error
	Delete(projectID, appID, id string) error
}

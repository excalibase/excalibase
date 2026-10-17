package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"golang.org/x/net/publicsuffix"
)

var (
	ErrHostnameNotFound = errors.New("custom domain not found")
	// ErrHostnameTaken is a hostname another site, of any project, already holds.
	ErrHostnameTaken        = errors.New("that domain is already added to a site")
	ErrHostnameLimitReached = errors.New("the project already holds as many custom domains as it may")
	// ErrCustomDomainsUnavailable is an install whose Cloudflare zone is not set up for custom hostnames.
	ErrCustomDomainsUnavailable = errors.New("custom domains are not available on this install")
	// ErrCustomDomainsFull is the install's zone holding as many custom hostnames as it may.
	ErrCustomDomainsFull = errors.New("this install has no room for another custom domain right now; try again later or ask its operator")
)

// maxHostnameLength leaves room for the challenge label within DNS's 253 characters.
const maxHostnameLength = 253 - len(verificationLabel)

// Custom hostname statuses: ACTIVE once Cloudflare proxies the hostname and its certificate is deployed.
const (
	HostnamePending = "PENDING"
	HostnameActive  = "ACTIVE"
	HostnameFailed  = "FAILED"
)

// What each DNS record a user adds is for.
const (
	PurposeRouting = "routing"
	// PurposeVerification is the platform's own proof that whoever adds the domain controls it.
	PurposeVerification = "verification"
	PurposeOwnership    = "ownership"
	PurposeCertificate  = "certificate"
)

const (
	verificationLabel  = "_excalibase-challenge."
	verificationPrefix = "excalibase-verification="
)

// VerificationName is the TXT record that proves control of hostname.
func VerificationName(hostname string) string { return verificationLabel + hostname }

// excalibaseLabel is the registered-domain label no customer hostname may carry.
const excalibaseLabel = "excalibase"

// CustomHostname is a customer's own domain serving one site, through Cloudflare for SaaS.
type CustomHostname struct {
	ID        string `json:"id"`
	SiteID    string `json:"siteId"`
	ProjectID string `json:"projectId"`
	Hostname  string `json:"hostname"`
	// Validation is how the certificate's domain control is proven: http or txt.
	Validation string `json:"validation"`
	Status     string `json:"status"`
	// CloudflareID is Cloudflare's id for the hostname; empty until its create was answered.
	CloudflareID string `json:"-"`
	// HostnameStatus and CertificateStatus are Cloudflare's own words, for the curious.
	HostnameStatus    string           `json:"hostnameStatus"`
	CertificateStatus string           `json:"certificateStatus"`
	Records           []DNSInstruction `json:"records"`
	Errors            []string         `json:"errors"`
	CreatedBy         string           `json:"createdBy"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	CheckedAt         time.Time        `json:"checkedAt"`
	// VerificationToken is what the owner's TXT record must hold; VerifiedAt is when it was seen.
	VerificationToken string     `json:"-"`
	VerifiedAt        *time.Time `json:"verifiedAt,omitempty"`
	// InactiveSince is when it was added or stopped serving; nil while active.
	InactiveSince *time.Time `json:"-"`
	// WithdrawnAt is when it last left the zone; it is not proven again within WithdrawnHostnameCooldown.
	WithdrawnAt *time.Time `json:"-"`
}

// DNSInstruction is one record the user adds at their DNS provider.
type DNSInstruction struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Purpose string `json:"purpose"`
}

// HostnameStore persists custom hostnames. Every lookup is scoped by project
// and site. A hostname is held by its one proven row; unproven claims hold nothing.
type HostnameStore interface {
	// CreateHostname refuses with ErrHostnameTaken when the project already
	// claims the hostname or any project proved it, and with
	// ErrHostnameLimitReached past maxPerProject claims, proven or not.
	CreateHostname(ctx context.Context, hostname *CustomHostname, maxPerProject int) error
	// VerifyHostname records the proof at hostname.VerifiedAt: ErrHostnameTaken
	// when another row holds a proof for it, ErrCustomDomainsFull once
	// maxOnZone rows hold one.
	VerifyHostname(ctx context.Context, hostname *CustomHostname, maxOnZone int) error
	// CountVerifiedHostnames counts proven rows: every hostname on the zone is one.
	CountVerifiedHostnames(ctx context.Context) (int, error)
	// GetHostname answers nil, no error, when the site holds no such hostname.
	GetHostname(ctx context.Context, projectID, siteID, id string) (*CustomHostname, error)
	ListHostnames(ctx context.Context, projectID, siteID string) ([]*CustomHostname, error)
	ListProjectHostnames(ctx context.Context, projectID string) ([]*CustomHostname, error)
	// UpdateHostname writes the Cloudflare id and state; ErrHostnameNotFound once it is gone.
	UpdateHostname(ctx context.Context, hostname *CustomHostname) error
	DeleteHostname(ctx context.Context, projectID, siteID, id string) error
	// DueHostnames answers hostnames not active, and active ones last checked
	// before activeCheckedBefore, least recently checked first, leaving out
	// projects scheduled for deletion.
	DueHostnames(ctx context.Context, activeCheckedBefore time.Time, limit int) ([]*CustomHostname, error)
}

// HostnameAPI is Cloudflare for SaaS on the user-content zone.
type HostnameAPI interface {
	CreateCustomHostname(ctx context.Context, hostname, method string) (cfworkers.CustomHostname, error)
	GetCustomHostname(ctx context.Context, id string) (cfworkers.CustomHostname, error)
	RevalidateCustomHostname(ctx context.Context, id, method string) (cfworkers.CustomHostname, error)
	FindCustomHostname(ctx context.Context, hostname string) (*cfworkers.CustomHostname, error)
	DeleteCustomHostname(ctx context.Context, id string) error
}

// NormalizeHostname takes the host out of what a user pastes and refuses an
// address, a registered domain or public suffix, a wildcard, anything under
// the user-content domain or a reserved domain, and any excalibase.* domain.
func NormalizeHostname(raw, baseDomain string, reserved []string) (string, error) {
	host, err := apphost.NormalizeCustomDomain(raw, baseDomain)
	if err != nil {
		return "", errors.New(strings.Replace(err.Error(), apphost.ErrInvalidDomain.Error()+": ", "", 1))
	}
	if len(host) > maxHostnameLength {
		return "", fmt.Errorf("a custom domain is at most %d characters", maxHostnameLength)
	}
	// The router and its hosts table take only letter top-level domains.
	if tld := host[strings.LastIndex(host, ".")+1:]; strings.Trim(tld, "abcdefghijklmnopqrstuvwxyz") != "" {
		return "", fmt.Errorf("%q is under an internationalised top-level domain, which is not supported", host)
	}
	registered, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", fmt.Errorf("%q is not a host name", host)
	}
	if label, _, _ := strings.Cut(registered, "."); label == excalibaseLabel {
		return "", fmt.Errorf("%q is a domain of the platform's own", host)
	}
	for _, domain := range reserved {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			return "", fmt.Errorf("%q is under the platform's own domain", host)
		}
	}
	return host, nil
}

// hostnameStatus is ACTIVE only when Cloudflare proxies the hostname and serves its certificate.
func hostnameStatus(answer cfworkers.CustomHostname) string {
	switch answer.Status {
	case "blocked", "pending_blocked", "moved", "deleted", "pending_deletion", "test_blocked", "test_failed":
		return HostnameFailed
	}
	certificate := answer.SSL.Status
	if strings.HasSuffix(certificate, "_timed_out") || certificate == "expired" || certificate == "deleted" || certificate == "inactive" {
		return HostnameFailed
	}
	// active_redeploying is an active hostname whose settings Cloudflare is rolling out again.
	if (answer.Status == "active" || answer.Status == "active_redeploying") && certificate == "active" {
		return HostnameActive
	}
	return HostnamePending
}

// dnsInstructions is the CNAME every custom hostname needs, the platform's own
// TXT proof, and, for TXT validation, Cloudflare's tokens
// still to prove. HTTP validation needs nothing more: Cloudflare answers its
// own token once the CNAME is in place.
func dnsInstructions(hostname *CustomHostname, target string, answer cfworkers.CustomHostname) []DNSInstruction {
	// The platform's own proof is checked again while the domain serves, so it stays listed.
	records := []DNSInstruction{
		{Type: "CNAME", Name: hostname.Hostname, Value: target, Purpose: PurposeRouting},
		{Type: "TXT", Name: VerificationName(hostname.Hostname), Value: verificationPrefix + hostname.VerificationToken, Purpose: PurposeVerification},
	}
	if hostname.Validation != cfworkers.ValidationTXT {
		return records
	}
	if ownership := answer.OwnershipVerification; answer.Status != "active" && ownership.Name != "" && ownership.Value != "" {
		records = append(records, DNSInstruction{Type: "TXT", Name: ownership.Name, Value: ownership.Value, Purpose: PurposeOwnership})
	}
	if answer.SSL.Status == "active" {
		return records
	}
	for _, token := range answer.SSL.ValidationRecords {
		if token.TXTName != "" && token.TXTValue != "" {
			records = append(records, DNSInstruction{Type: "TXT", Name: token.TXTName, Value: token.TXTValue, Purpose: PurposeCertificate})
		}
	}
	return records
}

// hostnameErrors are what Cloudflare and the certificate authority last said was wrong.
func hostnameErrors(answer cfworkers.CustomHostname) []string {
	out := []string{}
	for _, message := range answer.VerificationErrors {
		if message != "" {
			out = append(out, message)
		}
	}
	for _, failure := range answer.SSL.ValidationErrors {
		if failure.Message != "" {
			out = append(out, failure.Message)
		}
	}
	return out
}

// HostnameCorsOwner is the key the project's CORS list records a custom hostname's origin under.
func HostnameCorsOwner(hostname *CustomHostname) string { return "sitehost-" + hostname.ID }

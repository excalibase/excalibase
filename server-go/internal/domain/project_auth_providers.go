package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// End-user OAuth sign-in for a project's own app (EXC-662). The customer
// registers their own OAuth app at the provider; its client id is stored here
// and its secret only in the vault.
const (
	AuthProviderGoogle = "google"
	AuthProviderGitHub = "github"

	MaxAuthProviderClientIDLength     = 256
	MaxAuthProviderClientSecretLength = 512
	// MaxRedirectURLs caps a project's redirect allow-list.
	MaxRedirectURLs = 20
)

// ErrInvalidAuthProvider marks a provider setting the API refuses.
var ErrInvalidAuthProvider = errors.New("invalid sign-in provider setting")

// ErrInvalidRedirectURLs marks a redirect allow-list the API refuses.
var ErrInvalidRedirectURLs = errors.New("invalid redirect urls")

// AuthProviderNames is every provider, in the order Studio and the API list them.
func AuthProviderNames() []string {
	return []string{AuthProviderGoogle, AuthProviderGitHub}
}

// IsAuthProvider reports whether name is a supported provider.
func IsAuthProvider(name string) bool {
	for _, provider := range AuthProviderNames() {
		if provider == name {
			return true
		}
	}
	return false
}

// ProjectAuthProvider is one provider's stored settings. The secret itself is
// never here; ClientSecretSet says the vault holds one.
type ProjectAuthProvider struct {
	Provider        string
	Enabled         bool
	ClientID        string
	ClientSecretSet bool
	UpdatedAt       time.Time
}

// OAuthProviderInfo is a provider the auth service may offer, as /info lists it.
type OAuthProviderInfo struct {
	Provider    string `json:"provider"`
	ClientID    string `json:"clientId"`
	CallbackURL string `json:"callbackUrl"`
}

// ProjectAuthSignIn is the sign-in policy shipped with the oauth settings:
// where auth may send a user back to, and whether new users may sign up.
type ProjectAuthSignIn struct {
	RedirectURLs  []string
	AllowNewUsers bool
	// EmailOTPEnabled is the project's switch for sign-in by emailed link or code.
	EmailOTPEnabled bool
	// GuestSignInEnabled is the project's switch for guest accounts.
	GuestSignInEnabled bool
	// GuestCaptchaSecretSet says the vault holds a Turnstile secret, which
	// makes guest sign-up require a captcha.
	GuestCaptchaSecretSet bool
	// MFAEnabled is the project's switch for end-user TOTP factors; the
	// vault holds the key that seals them whenever it is on.
	MFAEnabled bool
}

// DefaultAuthSignIn is what a project without a settings row gets.
func DefaultAuthSignIn() ProjectAuthSignIn {
	return ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true}
}

// AuthProviderCallbackURL is the address the customer registers at the
// provider; the auth service serves it behind the public API host. Without
// an absolute base there is none, so no provider can be offered.
func AuthProviderCallbackURL(publicBaseURL, orgSlug, projectID, provider string) string {
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return ""
	}
	return base + "/auth/" + orgSlug + "/" + projectID + "/callback/" + provider
}

// AuthProviderSecretPath is the vault path of a provider's client secret. It
// sits under projects/{id}/ so deleting the project purges it.
func AuthProviderSecretPath(projectID, provider string) string {
	return "projects/" + projectID + "/auth/providers/" + provider
}

// ValidateAuthProviderClientID trims the id and refuses one no provider
// issues. A blank id reads as unset.
func ValidateAuthProviderClientID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if len(id) > MaxAuthProviderClientIDLength {
		return "", fmt.Errorf("%w: clientId is longer than %d characters", ErrInvalidAuthProvider, MaxAuthProviderClientIDLength)
	}
	if strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("%w: clientId must not contain spaces or control characters", ErrInvalidAuthProvider)
	}
	return id, nil
}

// ValidateAuthProviderClientSecret refuses an empty, oversized or
// control-character secret; the message never repeats it.
func ValidateAuthProviderClientSecret(secret string) error {
	if secret == "" || len(secret) > MaxAuthProviderClientSecretLength {
		return fmt.Errorf("%w: clientSecret must be 1 to %d characters", ErrInvalidAuthProvider, MaxAuthProviderClientSecretLength)
	}
	if strings.IndexFunc(secret, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: clientSecret must not contain control characters", ErrInvalidAuthProvider)
	}
	return nil
}

// NormalizeRedirectURLs canonicalises each entry the way the site URL is,
// drops duplicates keeping the first, and caps the list.
func NormalizeRedirectURLs(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, entry := range raw {
		canonical, err := validateRedirectURL(entry)
		if err != nil {
			return nil, err
		}
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	}
	if len(out) > MaxRedirectURLs {
		return nil, fmt.Errorf("%w: at most %d entries", ErrInvalidRedirectURLs, MaxRedirectURLs)
	}
	return out, nil
}

// AllAuthProviders returns one entry per supported provider in list order;
// a provider with no stored row reads as switched off and unconfigured.
func AllAuthProviders(stored []ProjectAuthProvider) []ProjectAuthProvider {
	byName := make(map[string]ProjectAuthProvider, len(stored))
	for _, provider := range stored {
		byName[provider.Provider] = provider
	}
	out := make([]ProjectAuthProvider, 0, len(AuthProviderNames()))
	for _, name := range AuthProviderNames() {
		provider, ok := byName[name]
		if !ok {
			provider = ProjectAuthProvider{Provider: name}
		}
		out = append(out, provider)
	}
	return out
}

// UsableOAuthProviders lists the providers auth may offer: switched on, with a
// client id and a secret in the vault. Never nil.
func UsableOAuthProviders(stored []ProjectAuthProvider, publicBaseURL, orgSlug, projectID string) []OAuthProviderInfo {
	out := []OAuthProviderInfo{}
	for _, provider := range AllAuthProviders(stored) {
		callback := AuthProviderCallbackURL(publicBaseURL, orgSlug, projectID, provider.Provider)
		if !provider.Enabled || provider.ClientID == "" || !provider.ClientSecretSet || callback == "" {
			continue
		}
		out = append(out, OAuthProviderInfo{Provider: provider.Provider, ClientID: provider.ClientID, CallbackURL: callback})
	}
	return out
}

// validateRedirectURL is ValidateSiteURL plus what a redirect target must not
// carry: a wildcard host, an out-of-range port, control characters or dot
// segments, raw or encoded. The path is kept escaped, as auth compares it.
func validateRedirectURL(entry string) (string, error) {
	if _, err := ValidateSiteURL(entry); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidRedirectURLs, err.Error())
	}
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return "", fmt.Errorf("%w: an entry is empty", ErrInvalidRedirectURLs)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !plainRedirect(parsed) {
		return "", fmt.Errorf("%w: %q must name one host and a plain path", ErrInvalidRedirectURLs, truncateForError(trimmed))
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + parsed.EscapedPath(), nil
}

func plainRedirect(parsed *url.URL) bool {
	if strings.Contains(parsed.Host, "*") {
		return false
	}
	if port := parsed.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if strings.IndexFunc(parsed.Path, unicode.IsControl) >= 0 {
		return false
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "/") {
			return false
		}
	}
	return !strings.Contains(strings.ToLower(parsed.EscapedPath()), "%2f")
}

// Daily budgets of the relay's project mail (EXC-662): sign-in links and codes
// are counted apart from account notices, so neither can use up the other.
const (
	MailBudgetSignIn  = "sign_in"
	MailBudgetAccount = "account"
)

// SignInMailProject is what an end-user sign-in mail says about the project
// that sent it, and whether the project signs in by mail at all.
type SignInMailProject struct {
	// Exists is false for a project the platform does not know.
	Exists          bool
	Name            string
	SiteURL         string
	EmailOTPEnabled bool
	// OAuthEnabled is whether any Google/GitHub sign-in row is switched on.
	OAuthEnabled bool
}

// GuestCaptchaSecretPath is the vault path of the project's Turnstile secret,
// under projects/{id}/ so deleting the project purges it.
func GuestCaptchaSecretPath(projectID string) string {
	return "projects/" + projectID + "/auth/captcha"
}

// ErrInvalidGuestCaptchaSecret refuses a secret that is empty, too long or not printable ASCII.
var ErrInvalidGuestCaptchaSecret = errors.New("the Turnstile secret key must be 1 to 256 printable characters without spaces")

// ValidateGuestCaptchaSecret checks a Turnstile secret key's shape.
func ValidateGuestCaptchaSecret(secret string) error {
	if secret == "" || len(secret) > 256 {
		return ErrInvalidGuestCaptchaSecret
	}
	for _, c := range secret {
		if c <= ' ' || c > '~' {
			return ErrInvalidGuestCaptchaSecret
		}
	}
	return nil
}

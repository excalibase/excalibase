package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// The end-user auth hooks (EXC-662 step 6) the auth service calls.
const (
	AuthHookCustomAccessToken = "custom_access_token"
	AuthHookBeforeUserCreated = "before_user_created"
	AuthHookSendEmail         = "send_email"

	authHookSecretPrefix = "whsec_"
	authHookSecretBytes  = 32
	maxAuthHookURLLength = 2048
)

var authHookNames = []string{AuthHookCustomAccessToken, AuthHookBeforeUserCreated, AuthHookSendEmail}

// ErrInvalidAuthHookURL refuses a hook URL that is not one public https endpoint.
var ErrInvalidAuthHookURL = errors.New("the hook URL must be an https:// URL of at most 2048 characters naming one public host on port 443, with no user:password or #fragment")

// ErrAuthHookURLPort refuses a hook URL on any port but the https one.
var ErrAuthHookURLPort = fmt.Errorf("%w: only port 443 is allowed", ErrInvalidAuthHookURL)

// ErrAuthHookNeedsSecret refuses switching a hook on before the project has a signing secret.
var ErrAuthHookNeedsSecret = errors.New("generate a signing secret before enabling a hook")

// ProjectAuthHook is one hook's setting. The signing secret is never here:
// it lives in the vault at AuthHooksSecretPath.
type ProjectAuthHook struct {
	Hook      string
	Enabled   bool
	URL       string
	UpdatedAt time.Time
}

// AuthHookNames lists every hook, in the order the API answers them.
func AuthHookNames() []string {
	return append([]string(nil), authHookNames...)
}

// IsAuthHook reports whether name is a hook the platform supports.
func IsAuthHook(name string) bool {
	for _, known := range authHookNames {
		if known == name {
			return true
		}
	}
	return false
}

// AllAuthHooks returns every hook in order, the stored row where there is one
// and an unset, switched-off hook where there is none.
func AllAuthHooks(stored []ProjectAuthHook) []ProjectAuthHook {
	byName := make(map[string]ProjectAuthHook, len(stored))
	for _, hook := range stored {
		byName[hook.Hook] = hook
	}
	out := make([]ProjectAuthHook, 0, len(authHookNames))
	for _, name := range authHookNames {
		hook, ok := byName[name]
		if !ok {
			hook = ProjectAuthHook{Hook: name}
		}
		out = append(out, hook)
	}
	return out
}

// AuthHooksSecretPath is the vault path of the project's hook signing secret,
// under projects/{id}/ so deleting the project purges it.
func AuthHooksSecretPath(projectID string) string {
	return "projects/" + projectID + "/auth/hooks"
}

// ValidateAuthHookURL accepts an https URL naming one public host (an IP
// literal must be public), on port 443 only, no credentials or fragment, and returns
// it with the host lower-cased. The auth service checks every resolved
// address again when it connects.
func ValidateAuthHookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxAuthHookURLLength || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", ErrInvalidAuthHookURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return "", ErrInvalidAuthHookURL
	}
	if parsed.User != nil || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", ErrInvalidAuthHookURL
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", ErrAuthHookURLPort
	}
	host := strings.ToLower(parsed.Hostname())
	if err := checkAuthHookHost(host); err != nil {
		return "", err
	}
	parsed.Host = joinAuthHookHost(host, parsed.Port())
	return parsed.String(), nil
}

func joinAuthHookHost(host, port string) string {
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func checkAuthHookHost(host string) error {
	if err := publicaddr.ValidateHostSyntax(host); err != nil {
		return ErrInvalidAuthHookURL
	}
	addr, err := netip.ParseAddr(host)
	if err == nil {
		if publicaddr.ClassifyAddr(addr) != nil {
			return ErrInvalidAuthHookURL
		}
		return nil
	}
	if numericTopLabel(host) {
		return ErrInvalidAuthHookURL
	}
	return nil
}

// numericTopLabel catches names some resolvers read as IPv4 (127.1, 0x7f.1):
// no real top-level domain is all digits or starts with 0x.
func numericTopLabel(host string) bool {
	name := strings.TrimSuffix(host, ".")
	top := name[strings.LastIndex(name, ".")+1:]
	if strings.HasPrefix(top, "0x") {
		return true
	}
	return strings.Trim(top, "0123456789") == ""
}

// AuthHookURLHost is the host (and port) of a hook URL, which is all an audit
// row records: a path or query string may carry the receiver's token.
func AuthHookURLHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// GenerateAuthHookSecret returns a fresh Standard Webhooks signing secret:
// "whsec_" and the base64 of 32 random bytes.
func GenerateAuthHookSecret() (string, error) {
	return generateAuthHookSecret(rand.Read)
}

func generateAuthHookSecret(read func([]byte) (int, error)) (string, error) {
	key := make([]byte, authHookSecretBytes)
	if _, err := read(key); err != nil {
		return "", fmt.Errorf("generate the hook signing secret: %w", err)
	}
	return authHookSecretPrefix + base64.StdEncoding.EncodeToString(key), nil
}

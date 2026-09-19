package auth

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Capability is one entry of a token's permission list. The wire grammar is
// <resource>:<action>[:<selector>] — for example "policies:read",
// "projects:info:read" or "vault:read:pki/signing/*".
//
// A token that carries at least one capability is a *capability token*: it
// reaches only the handlers that declare a matching capability, whatever the
// owning user's platform role says. Tokens with an empty list are ordinary
// human PATs and sessions, unaffected by this layer.
type Capability struct {
	Resource string
	Action   string
	// Selector narrows the action to a subject — a vault secret path, say.
	// Empty means the capability names no subject at all; it never acts as
	// a wildcard.
	Selector string
}

const (
	capabilitySeparator = ":"
	selectorWildcard    = "*"
	pathSeparator       = "/"
	// SelfCapability is granted implicitly to every capability token so a
	// service can validate its own credential against /api/auth/me.
	selfResource = "self"
	selfAction   = "read"
)

var errMalformedCapability = errors.New("capability must be <resource>:<action>[:<selector>]")

// SelfCapability is the implicit grant every capability token carries.
func SelfCapability() Capability {
	return Capability{Resource: selfResource, Action: selfAction}
}

// ParseCapability validates one permission string and returns it structured.
func ParseCapability(raw string) (Capability, error) {
	parts := strings.Split(strings.TrimSpace(raw), capabilitySeparator)
	if len(parts) < 2 || len(parts) > 3 {
		return Capability{}, errMalformedCapability
	}
	parsed := Capability{Resource: parts[0], Action: parts[1]}
	if len(parts) == 3 {
		parsed.Selector = parts[2]
	}
	if err := validateName(parsed.Resource); err != nil {
		return Capability{}, fmt.Errorf("resource: %w", err)
	}
	if err := validateName(parsed.Action); err != nil {
		return Capability{}, fmt.Errorf("action: %w", err)
	}
	if err := validateSelector(parsed.Selector); err != nil {
		return Capability{}, fmt.Errorf("selector: %w", err)
	}
	return parsed, nil
}

// validateName accepts a lowercase identifier: the resource and action parts
// are a closed vocabulary, never a pattern.
func validateName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return errors.New("must be lowercase letters, digits, '_' or '-'")
		}
	}
	return nil
}

// validateSelector accepts a literal subject, optionally ending in a single
// '*' that stands for one path segment. Interior wildcards and parent
// traversal are rejected so a stored permission can never be read as broader
// than it looks.
func validateSelector(selector string) error {
	if selector == "" {
		return nil
	}
	if strings.Contains(selector, "..") {
		return errors.New("must not contain '..'")
	}
	stars := strings.Count(selector, selectorWildcard)
	if stars == 0 {
		return nil
	}
	if stars > 1 || !strings.HasSuffix(selector, selectorWildcard) {
		return errors.New("'*' is only allowed as the final character")
	}
	return nil
}

// String renders the capability back to its wire form.
func (c Capability) String() string {
	parts := []string{c.Resource, c.Action}
	if c.Selector != "" {
		parts = append(parts, c.Selector)
	}
	return strings.Join(parts, capabilitySeparator)
}

// Grants reports whether c (a granted capability) covers want (the capability
// a handler demands). Resource and action must be equal; the selector matches
// literally, or through a trailing '*' that stands for exactly one non-empty
// path segment.
func (c Capability) Grants(want Capability) bool {
	if c.Resource != want.Resource || c.Action != want.Action {
		return false
	}
	if !strings.HasSuffix(c.Selector, selectorWildcard) {
		return c.Selector == want.Selector
	}
	prefix := strings.TrimSuffix(c.Selector, selectorWildcard)
	if !strings.HasPrefix(want.Selector, prefix) {
		return false
	}
	leaf := want.Selector[len(prefix):]
	return leaf != "" && !strings.Contains(leaf, pathSeparator)
}

// NormalizeCapabilities validates a requested permission list and returns it
// trimmed, deduplicated and sorted. A nil or empty list yields an empty
// slice — an ordinary, non-capability token.
func NormalizeCapabilities(requested []string) ([]string, error) {
	seen := make(map[string]bool, len(requested))
	for _, raw := range requested {
		parsed, err := ParseCapability(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid permission %q: %w", strings.TrimSpace(raw), err)
		}
		seen[parsed.String()] = true
	}
	out := make([]string, 0, len(seen))
	for entry := range seen {
		out = append(out, entry)
	}
	sort.Strings(out)
	return out, nil
}

// IsCapabilityToken reports whether the token is restricted to an explicit
// permission list.
func IsCapabilityToken(t *domain.AccessToken) bool {
	return t != nil && len(t.Permissions) > 0
}

// TokenGrants reports whether the capability token lists a permission that
// covers want. Entries that no longer parse are ignored so one bad row can
// neither open the gate nor close it on the others.
func TokenGrants(t *domain.AccessToken, want Capability) bool {
	if !IsCapabilityToken(t) {
		return false
	}
	if want == SelfCapability() {
		return true
	}
	for _, raw := range t.Permissions {
		granted, err := ParseCapability(raw)
		if err != nil {
			continue
		}
		if granted.Grants(want) {
			return true
		}
	}
	return false
}

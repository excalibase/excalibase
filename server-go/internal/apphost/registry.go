package apphost

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// dockerHub is the one name every spelling of Docker Hub is stored under.
const dockerHub = "docker.io"

const (
	MaxRegistryHostLength     = 253
	MaxRegistryUsernameLength = 256
	// MaxRegistryPasswordLength leaves room for the longest short-lived tokens registries issue.
	MaxRegistryPasswordLength = 8192
)

var dockerHubAliases = map[string]bool{
	dockerHub: true, "index.docker.io": true, "registry-1.docker.io": true,
}

// RegistryCredential is what a project stores to pull from a private registry.
// It lives only in the project's vault; nothing returns it.
type RegistryCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Validate refuses what a registry login could not carry: a colon ends the
// username in the basic-auth pair, and control characters are never part of one.
func (c RegistryCredential) Validate() error {
	switch {
	case c.Username == "" || c.Password == "":
		return errors.New("a username and a password or token are both required")
	case len(c.Username) > MaxRegistryUsernameLength:
		return fmt.Errorf("the username exceeds %d characters", MaxRegistryUsernameLength)
	case len(c.Password) > MaxRegistryPasswordLength:
		return fmt.Errorf("the password exceeds %d characters", MaxRegistryPasswordLength)
	case strings.Contains(c.Username, ":"):
		return errors.New("the username must not contain a colon")
	case hasControl(c.Username) || hasControl(c.Password):
		return errors.New("the credential must not contain control characters")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// NormalizeRegistry checks a registry host (with an optional port) and
// returns the one spelling it is stored and matched under.
func NormalizeRegistry(raw string) (string, error) {
	host := strings.ToLower(raw)
	if host == "" || len(host) > MaxRegistryHostLength || !imageHost.MatchString(host) {
		return "", fmt.Errorf("invalid registry %q: give a host name, with a port if it needs one", raw)
	}
	if _, port, found := strings.Cut(host, ":"); found && !validPort(port) {
		return "", fmt.Errorf("invalid registry %q: the port must be between 1 and 65535", raw)
	}
	if dockerHubAliases[host] {
		return dockerHub, nil
	}
	return host, nil
}

func validPort(port string) bool {
	n := 0
	for _, r := range port {
		n = n*10 + int(r-'0')
	}
	return n >= 1 && n <= 65535
}

// ImageRegistry names the registry an image reference is pulled from, the way
// a container runtime reads it: no registry component means Docker Hub.
func ImageRegistry(image string) string {
	name, _, _ := strings.Cut(image, "@")
	first, _, hasPath := strings.Cut(name, "/")
	if !hasPath || !isRegistryHost(first) {
		return dockerHub
	}
	host := strings.ToLower(first)
	if dockerHubAliases[host] {
		return dockerHub
	}
	return host
}

// RegistryCredentialPrefix holds every registry credential of one project.
func RegistryCredentialPrefix(projectID string) string {
	return "projects/" + projectID + "/registries/"
}

// RegistryCredentialPath is where one registry's credential is kept; registry must already be normalized.
func RegistryCredentialPath(projectID, registry string) string {
	return RegistryCredentialPrefix(projectID) + registry
}

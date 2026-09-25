package apphost

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ErrNoRoute marks an app that cannot be given a hostname.
var ErrNoRoute = errors.New("app route")

// platformProjectPrefix is dropped from the hostname: every platform project id carries it.
const platformProjectPrefix = "proj-"

// Route is where apps are reachable: <app-name>-<short-project-id>.<Domain>.
type Route struct {
	Domain string
	TLS    bool
}

// Hostname is unique because the app name is unique per project and the project id is unique.
func (r Route) Hostname(appName, projectID string) (string, error) {
	if r.Domain == "" {
		return "", fmt.Errorf("%w: no app domain configured", ErrNoRoute)
	}
	if err := ValidateName(appName); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoRoute, err)
	}
	label := appName + "-" + strings.TrimPrefix(projectID, platformProjectPrefix)
	if problems := validation.IsDNS1123Label(label); len(problems) > 0 {
		return "", fmt.Errorf("%w: %q is not a DNS label: %s", ErrNoRoute, label, strings.Join(problems, "; "))
	}
	host := label + "." + r.Domain
	if problems := validation.IsDNS1123Subdomain(host); len(problems) > 0 {
		return "", fmt.Errorf("%w: %q is not a hostname: %s", ErrNoRoute, host, strings.Join(problems, "; "))
	}
	return host, nil
}

func (r Route) URL(appName, projectID string) (string, error) {
	host, err := r.Hostname(appName, projectID)
	if err != nil {
		return "", err
	}
	scheme := "https"
	if !r.TLS {
		scheme = "http"
	}
	return (&url.URL{Scheme: scheme, Host: host}).String(), nil
}

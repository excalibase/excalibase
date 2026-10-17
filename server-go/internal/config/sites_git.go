package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// SiteGitConfig is the GitHub App and build cluster settings git builds use
// (EXC-354, ADR 0043 decision 8). The App itself is created by the owner.
type SiteGitConfig struct {
	AppID   int64
	AppSlug string
	// ClientID and ClientSecret verify, through GitHub sign-in, which installations and repos a user may connect.
	ClientID     string
	ClientSecret string
	// PrivateKeyPEM signs the App's JWTs; read from SITES_GITHUB_APP_PRIVATE_KEY or the _FILE a Secret is mounted at.
	PrivateKeyPEM string
	WebhookSecret string
	// APIURL and WebURL are github.com's unless an install points them elsewhere.
	APIURL string
	WebURL string
	// BuildNamespace holds every build Job; the chart creates it and the builder's Role with features.sitesGitBuilds.
	BuildNamespace string
	// BuildSlots is how many builds run at once on this install, across every project.
	BuildSlots int
	// PriorityClass is the build pods' class, below every tenant workload; required in a cluster.
	PriorityClass string
	// NodeImage is a mirror of the node image repository builds run in (<NodeImage>:<major>-bookworm);
	// "" runs the digest-pinned Docker Hub images.
	NodeImage string
	// PrepareImage runs only the workdir chmod; a minimal image pinned by digest.
	PrepareImage string

	keyFileErr error
	appIDErr   bool
}

// DefaultSiteBuildPrepareImage is busybox, pinned so a moved tag never changes what runs as root in a build pod.
const DefaultSiteBuildPrepareImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func loadSiteGit() SiteGitConfig {
	git := SiteGitConfig{
		AppSlug:        strings.TrimSpace(os.Getenv("SITES_GITHUB_APP_SLUG")),
		ClientID:       strings.TrimSpace(os.Getenv("SITES_GITHUB_CLIENT_ID")),
		ClientSecret:   strings.TrimSpace(os.Getenv("SITES_GITHUB_CLIENT_SECRET")),
		PrivateKeyPEM:  strings.TrimSpace(os.Getenv("SITES_GITHUB_APP_PRIVATE_KEY")),
		WebhookSecret:  strings.TrimSpace(os.Getenv("SITES_GITHUB_WEBHOOK_SECRET")),
		APIURL:         strings.TrimRight(envOr("SITES_GITHUB_API_URL", "https://api.github.com"), "/"),
		WebURL:         strings.TrimRight(envOr("SITES_GITHUB_WEB_URL", "https://github.com"), "/"),
		BuildNamespace: envOr("SITES_BUILD_NAMESPACE", "excalibase-site-builds"),
		BuildSlots:     envInt("SITES_BUILD_SLOTS", 1),
		PriorityClass:  strings.TrimSpace(os.Getenv("SITES_BUILD_PRIORITY_CLASS")),
		NodeImage:      strings.TrimSpace(os.Getenv("SITES_BUILD_NODE_IMAGE")),
		PrepareImage:   envOr("SITES_BUILD_PREPARE_IMAGE", DefaultSiteBuildPrepareImage),
	}
	if raw := strings.TrimSpace(os.Getenv("SITES_GITHUB_APP_ID")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		git.AppID, git.appIDErr = id, err != nil || id <= 0
	}
	if path := strings.TrimSpace(os.Getenv("SITES_GITHUB_APP_PRIVATE_KEY_FILE")); path != "" {
		key, err := os.ReadFile(path)
		git.PrivateKeyPEM, git.keyFileErr = strings.TrimSpace(string(key)), err
	}
	return git
}

// Complete reports whether every GitHub App setting is given.
func (g SiteGitConfig) Complete() bool {
	return g.AppID > 0 && g.AppSlug != "" && g.ClientID != "" && g.ClientSecret != "" && g.PrivateKeyPEM != "" && g.WebhookSecret != ""
}

func (g SiteGitConfig) String() string {
	return fmt.Sprintf("{AppID:%d AppSlug:%s ClientID:%s ClientSecret:[redacted] PrivateKey:[redacted] WebhookSecret:[redacted] "+
		"APIURL:%s WebURL:%s BuildNamespace:%s BuildSlots:%d PriorityClass:%s NodeImage:%s PrepareImage:%s}",
		g.AppID, g.AppSlug, g.ClientID, g.APIURL, g.WebURL, g.BuildNamespace, g.BuildSlots, g.PriorityClass, g.NodeImage, g.PrepareImage)
}

func (g SiteGitConfig) GoString() string { return "config.SiteGitConfig" + g.String() }

// SiteGitBuildsOn is whether git builds are switched on where Sites is offered at all.
func (c AppConfig) SiteGitBuildsOn() bool {
	return c.SitesGitBuildsEnabled && c.SitesAvailable()
}

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validateSiteGit refuses settings a build could not run with, once git builds are switched on.
func (c AppConfig) validateSiteGit() error {
	if !c.SitesGitBuildsEnabled {
		return nil
	}
	git := c.SiteGit
	if git.keyFileErr != nil {
		return fmt.Errorf("SITES_GITHUB_APP_PRIVATE_KEY_FILE: %w", git.keyFileErr)
	}
	if git.appIDErr {
		return errors.New("SITES_GITHUB_APP_ID must be the GitHub App's numeric id")
	}
	if git.BuildSlots < 1 {
		return errors.New("SITES_BUILD_SLOTS must be at least 1")
	}
	if c.ProvisionerMode != "docker" && strings.TrimSpace(git.PriorityClass) == "" {
		return errors.New("SITES_BUILD_PRIORITY_CLASS is required: builds must yield to tenants' workloads when a node runs short (the chart ships one with sites.enabled)")
	}
	if strings.TrimSpace(git.PrepareImage) == "" {
		return errors.New("SITES_BUILD_PREPARE_IMAGE must name an image")
	}
	if !namespacePattern.MatchString(git.BuildNamespace) {
		return fmt.Errorf("SITES_BUILD_NAMESPACE %q is not a namespace name", git.BuildNamespace)
	}
	for name, raw := range map[string]string{"SITES_GITHUB_API_URL": git.APIURL, "SITES_GITHUB_WEB_URL": git.WebURL} {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("%s %q is not an http(s) URL", name, raw)
		}
	}
	return nil
}

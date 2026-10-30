package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// AppRegistryConfig is the platform's own registry for git-built images
// (EXC-540): the host images name, and the key that signs its tokens.
type AppRegistryConfig struct {
	Host string
	// Aliases are the other names that reach the same registry (its Service's DNS names, its ClusterIP).
	Aliases []string
	// URL is the registry's in-cluster address, for provisioning's own calls (the sweep, the push).
	URL         string
	TokenKeyPEM []byte
	keyErr      error
	keyFile     string
}

func loadAppRegistry() AppRegistryConfig {
	registry := AppRegistryConfig{
		Host:    strings.ToLower(strings.TrimSpace(os.Getenv("APP_REGISTRY_HOST"))),
		keyFile: strings.TrimSpace(os.Getenv("APP_REGISTRY_TOKEN_KEY_FILE")),
		URL:     strings.TrimSpace(os.Getenv("APP_REGISTRY_URL")),
	}
	for _, alias := range strings.Split(os.Getenv("APP_REGISTRY_ALIASES"), ",") {
		if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" {
			registry.Aliases = append(registry.Aliases, alias)
		}
	}
	if registry.keyFile != "" {
		registry.TokenKeyPEM, registry.keyErr = os.ReadFile(registry.keyFile)
	}
	return registry
}

func (c AppConfig) validateAppRegistry() error {
	registry := c.AppRegistry
	if registry.Host == "" {
		return nil
	}
	if c.ProvisionerMode == "docker" {
		return errors.New("APP_REGISTRY_HOST is for Kubernetes installs; single-host app builds come later (EXC-677)")
	}
	for _, name := range append([]string{registry.Host}, registry.Aliases...) {
		if dockerHubHosts[name] || registryHost.FindStringSubmatch(name) == nil {
			return fmt.Errorf("APP_REGISTRY_HOST / APP_REGISTRY_ALIASES: %q is not a registry host the platform can own", name)
		}
		if slices.Contains(c.AppLocalRegistries, name) {
			return fmt.Errorf("APP_REGISTRY_HOST / APP_REGISTRY_ALIASES: %q is also in APP_LOCAL_REGISTRIES; an operator's registry is never the platform's", name)
		}
	}
	if registry.keyErr != nil {
		return fmt.Errorf("APP_REGISTRY_TOKEN_KEY_FILE: %w", registry.keyErr)
	}
	// The host alone only guards user images; issuing tokens needs the key.
	if !slices.Contains(c.DarkFeatures, features.AppGitBuilds) {
		return nil
	}
	if len(registry.TokenKeyPEM) == 0 {
		return errors.New("APP_REGISTRY_TOKEN_KEY_FILE is required with app git builds on: the registry trusts only tokens it signs")
	}
	if registry.URL == "" {
		return errors.New("APP_REGISTRY_URL is required with app git builds on: provisioning pushes and sweeps the registry there")
	}
	return nil
}

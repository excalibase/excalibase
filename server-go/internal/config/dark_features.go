package config

import (
	"os"
	"slices"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// featureEnv names the variable that turns each dark feature on (EXC-554);
// the chart sets it from features.<name>.
var featureEnv = map[features.Feature]string{
	features.MCP:             "FEATURE_MCP",
	features.Pipeline:        "FEATURE_PIPELINE",
	features.AppStorage:      "FEATURE_APPSTORAGE",
	features.MySQL:           "FEATURE_MYSQL",
	features.MySQLPITR:       "FEATURE_MYSQLPITR",
	features.SlowQueries:     "FEATURE_SLOWQUERIES",
	features.RequestMetrics:  "FEATURE_REQUESTMETRICS",
	features.Advisors:        "FEATURE_ADVISORS",
	features.MetricsEndpoint: "FEATURE_METRICSENDPOINT",
	features.LogRetention:    "FEATURE_LOGRETENTION",
	features.LogDrains:       "FEATURE_LOGDRAINS",
	features.AuthOAuth:       "FEATURE_AUTHOAUTH",
	features.AuthOTP:         "FEATURE_AUTHOTP",
	features.AuthUser:        "FEATURE_AUTHUSER",
	features.AuthGuest:       "FEATURE_AUTHGUEST",
	features.HostMetrics:     "FEATURE_HOSTMETRICS",
	features.HostLogs:        "FEATURE_HOSTLOGS",
}

// loadDarkFeatures reads once at startup; only the exact value "true" turns one on.
func loadDarkFeatures() []features.Feature {
	var on []features.Feature
	for _, feature := range features.All() {
		if os.Getenv(featureEnv[feature]) == "true" {
			on = append(on, feature)
		}
	}
	return on
}

// Features is the flags feature code asks; nothing else reads DarkFeatures.
// Host monitoring and host logs read a Docker or Podman host, so they are off
// on Kubernetes, which keeps logs in the chart's Loki. On a single host, log
// drains forward what host logs store, so they need it.
func (c AppConfig) Features() features.Flags {
	on := make([]features.Feature, 0, len(c.DarkFeatures))
	for _, feature := range c.DarkFeatures {
		if c.availableHere(feature) {
			on = append(on, feature)
		}
	}
	return features.NewStatic(on...)
}

func (c AppConfig) availableHere(feature features.Feature) bool {
	singleHost := c.ProvisionerMode == "docker"
	switch feature {
	case features.HostMetrics, features.HostLogs:
		return singleHost
	case features.LogDrains:
		return !singleHost || slices.Contains(c.DarkFeatures, features.HostLogs)
	default:
		return true
	}
}

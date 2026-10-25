package config

import (
	"os"

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
func (c AppConfig) Features() features.Flags {
	return features.NewStatic(c.DarkFeatures...)
}

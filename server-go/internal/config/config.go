package config

import (
	"log"
	"os"
	"strings"
)

type AppConfig struct {
	Port              string
	StoragePath       string
	LogLevel          string
	DBPath            string
	PlatformDBURL     string
	NatsURL           string
	CORSOrigins       []string
	WatcherChartPath  string
	DenoRuntimeURL    string
	DenoRuntimeSecret string
	DenoNamespace     string
	DenoRuntimeImage  string
	VaultURL          string
	VaultPAT          string
	DeploymentMode    string // "selfhosted" (default) or "cloud"
	PublicBaseURL     string // base URL for function invoke + SDK snippets, e.g. https://api.excalibase.io

	// K8s client connection — priority: remote API > kubeconfig path > env KUBECONFIG > in-cluster > ~/.kube/config
	KubeconfigPath        string // explicit kubeconfig file
	KubeAPIURL            string // remote API server URL (for out-of-cluster platform deployments)
	KubeBearerToken       string // ServiceAccount token for remote API
	KubeCACert            string // PEM-encoded CA cert for remote API TLS
	KubeInsecureSkipVerify bool  // disable TLS verification (dev only)
}

// IsCloud returns true when running in cloud deployment mode. Derived from
// DeploymentMode so the rest of the code has a single boolean to branch on.
// Cloud mode:
//   - Uses Postgres for platform store + vault backend (PLATFORM_DB_URL required)
//   - Enables multi-org create/delete + tier enforcement endpoints
//   - Enforces tier limits in the provisioning service
// Self-hosted mode (default):
//   - Uses SQLite for platform store, bbolt for vault
//   - Single default org, no tier enforcement, no billing endpoints
func (c AppConfig) IsCloud() bool {
	return c.DeploymentMode == "cloud"
}

func Load() AppConfig {
	return AppConfig{
		Port:             envOr("PORT", "24005"),
		StoragePath:      envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:         envOr("LOG_LEVEL", "debug"),
		DBPath:           envOr("DB_PATH", "../provisioning-data/excalibase.db"),
		PlatformDBURL:    envOr("PLATFORM_DB_URL", ""),
		NatsURL:          envOr("NATS_URL", ""),
		CORSOrigins:       parseCORSOrigins(envOr("CORS_ORIGINS", "https://app.excalibase.io")),
		WatcherChartPath:  envOr("WATCHER_CHART_PATH", "/charts/excalibase-watcher"),
		DenoRuntimeURL:    envOr("DENO_RUNTIME_URL", "http://deno-runtime.serverless.svc.cluster.local:8000"),
		DenoRuntimeSecret: envOr("DENO_RUNTIME_SECRET", ""),
		DenoNamespace:    envOr("DENO_NAMESPACE", "serverless"),
		DenoRuntimeImage: envOr("DENO_RUNTIME_IMAGE", "excalibase/deno-runtime:latest"),
		VaultURL:         envOr("VAULT_URL", ""),
		VaultPAT:         envOr("VAULT_PAT", ""),
		DeploymentMode:         envOr("DEPLOYMENT_MODE", "selfhosted"),
		PublicBaseURL:          envOr("PUBLIC_BASE_URL", "https://api.excalibase.io"),
		KubeconfigPath:         envOr("KUBECONFIG_PATH", ""),
		KubeAPIURL:             envOr("KUBE_API_URL", ""),
		KubeBearerToken:        envOr("KUBE_BEARER_TOKEN", ""),
		KubeCACert:             envOr("KUBE_CA_CERT", ""),
		KubeInsecureSkipVerify: envOr("KUBE_INSECURE_SKIP_VERIFY", "") == "true",
	}
}

func parseCORSOrigins(raw string) []string {
	if raw == "" {
		log.Fatal("CORS_ORIGINS must be set; refusing to start with no origin allowlist")
	}
	if raw == "*" {
		return []string{"*"}
	}
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			origins = append(origins, p)
		}
	}
	if len(origins) == 0 {
		log.Fatal("CORS_ORIGINS contained only empty values; refusing to start")
	}
	return origins
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

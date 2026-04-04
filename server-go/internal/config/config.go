package config

import (
	"os"
	"strings"
)

type AppConfig struct {
	Port             string
	StoragePath      string
	LogLevel         string
	DBPath           string
	CORSOrigins      []string
	DenoRuntimeURL    string
	DenoRuntimeSecret string
	DenoNamespace     string
	DenoRuntimeImage  string
}

func Load() AppConfig {
	return AppConfig{
		Port:             envOr("PORT", "24005"),
		StoragePath:      envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:         envOr("LOG_LEVEL", "debug"),
		DBPath:           envOr("DB_PATH", "../provisioning-data/excalibase.db"),
		CORSOrigins:      parseCORSOrigins(envOr("CORS_ORIGINS", "*")),
		DenoRuntimeURL:    envOr("DENO_RUNTIME_URL", "http://deno-runtime.serverless.svc.cluster.local:8000"),
		DenoRuntimeSecret: envOr("DENO_RUNTIME_SECRET", ""),
		DenoNamespace:    envOr("DENO_NAMESPACE", "serverless"),
		DenoRuntimeImage: envOr("DENO_RUNTIME_IMAGE", "excalibase/deno-runtime:latest"),
	}
}

func parseCORSOrigins(raw string) []string {
	if raw == "" || raw == "*" {
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
		return []string{"*"}
	}
	return origins
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

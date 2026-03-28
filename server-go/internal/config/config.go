package config

import "os"

type AppConfig struct {
	Port             string
	StoragePath      string
	LogLevel         string
	DenoRuntimeURL   string
	DenoNamespace    string
	DenoRuntimeImage string
}

func Load() AppConfig {
	return AppConfig{
		Port:             envOr("PORT", "24005"),
		StoragePath:      envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:         envOr("LOG_LEVEL", "debug"),
		DenoRuntimeURL:   envOr("DENO_RUNTIME_URL", "http://deno-runtime.serverless.svc.cluster.local:8000"),
		DenoNamespace:    envOr("DENO_NAMESPACE", "serverless"),
		DenoRuntimeImage: envOr("DENO_RUNTIME_IMAGE", "excalibase/deno-runtime:latest"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

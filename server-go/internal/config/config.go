package config

import "os"

type AppConfig struct {
	Port        string
	StoragePath string
	LogLevel    string
}

func Load() AppConfig {
	return AppConfig{
		Port:        envOr("PORT", "24005"),
		StoragePath: envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:    envOr("LOG_LEVEL", "debug"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

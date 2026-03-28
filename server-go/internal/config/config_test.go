package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Port != "24005" {
		t.Errorf("port: got %s, want 24005", cfg.Port)
	}
	if cfg.StoragePath != "../provisioning-data" {
		t.Errorf("storagePath: got %s", cfg.StoragePath)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("logLevel: got %s", cfg.LogLevel)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("PORT", "9999")
	os.Setenv("STORAGE_PATH", "/tmp/test-data")
	os.Setenv("LOG_LEVEL", "info")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("STORAGE_PATH")
		os.Unsetenv("LOG_LEVEL")
	}()

	cfg := Load()
	if cfg.Port != "9999" {
		t.Errorf("port: got %s, want 9999", cfg.Port)
	}
	if cfg.StoragePath != "/tmp/test-data" {
		t.Errorf("storagePath: got %s", cfg.StoragePath)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("logLevel: got %s", cfg.LogLevel)
	}
}
